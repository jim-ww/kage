package xmpp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"mellium.im/xmpp/disco"
	"mellium.im/xmpp/disco/items"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/upload"
)

// progressReader wraps an io.Reader, calling onProgress after every Read
// with cumulative bytes read so far and the (already-known) total. Left as a
// thin, unthrottled callback deliberately - throttling how often that's
// turned into UI updates is the caller's job, not this type's.
type progressReader struct {
	io.Reader
	total      int64
	sent       int64
	onProgress func(sent, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.Reader.Read(b)
	p.sent += int64(n)
	if p.onProgress != nil {
		p.onProgress(p.sent, p.total)
	}
	return n, err
}

// UploadFile uploads path using XEP-0363 HTTP File Upload and returns the
// service's download URL. The caller sends that URL as a normal message, which
// is both widely interoperable and lets recipients without attachment support
// still access the file.
func (c *Client) UploadFile(ctx context.Context, path string, onProgress func(sent, total int64)) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("statting %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening %q: %w", path, err)
	}
	defer f.Close()
	return c.Upload(ctx, filepath.Base(path), f, info.Size(), onProgress)
}

// Upload uploads size bytes of body as a file called name, like UploadFile.
// onProgress, if non-nil, is called with cumulative bytes sent as the PUT
// streams (not for the earlier disco/slot negotiation, which is
// comparatively instant).
func (c *Client) Upload(ctx context.Context, name string, body io.Reader, size int64, onProgress func(sent, total int64)) (string, error) {
	if size > int64(^uint(0)>>1) {
		return "", fmt.Errorf("%q is too large to upload", name)
	}
	// Service discovery and slot negotiation are tiny XMPP round trips. Keep
	// them tightly bounded so a server that does not implement XEP-0363 cannot
	// leave the UI waiting forever before the HTTP transfer even begins.
	discoveryCtx, cancelDiscovery := context.WithTimeout(ctx, 40*time.Second)
	defer cancelDiscovery()
	service, err := c.uploadService(discoveryCtx)
	if err != nil {
		return "", err
	}
	contentType := mime.TypeByExtension(filepath.Ext(name))
	slot, err := upload.GetSlot(discoveryCtx, upload.File{
		Name: name,
		Size: int(size),
		Type: contentType,
	}, service, c.session)
	if err != nil {
		return "", fmt.Errorf("requesting upload slot: %w", err)
	}
	if slot.PutURL == nil || slot.GetURL == nil {
		return "", fmt.Errorf("upload service returned an incomplete slot")
	}
	if onProgress != nil {
		body = &progressReader{Reader: body, total: size, onProgress: onProgress}
	}
	req, err := slot.Put(ctx, body)
	if err != nil {
		return "", fmt.Errorf("creating upload request: %w", err)
	}
	if contentType != "" {
		// slot.Put builds req.Header from slot.Header.Clone(); if the upload
		// service's slot response had no <header/> elements (the common case
		// — most services only send Authorization/Cookie when required),
		// slot.Header is nil and Clone() returns nil too, so req.Header must
		// be initialized before Set is called on it or this panics.
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		req.Header.Set("Content-Type", contentType)
	}
	// NewRequest cannot infer a length from an *os.File. Supplying it avoids
	// chunked transfer encoding, which a number of XEP-0363 services reject or
	// wait on indefinitely.
	req.ContentLength = size
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("uploading file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return "", fmt.Errorf("upload failed: HTTP %d", resp.StatusCode)
	}
	return slot.GetURL.String(), nil
}

// uploadService returns the JID of the account domain's XEP-0363 HTTP upload
// component, discovered via a disco walk the first time this is called and
// cached on c afterward — including a cached failure, so a server that just
// doesn't offer upload isn't re-walked on every single file send.
func (c *Client) uploadService(ctx context.Context) (jid.JID, error) {
	c.mu.Lock()
	if c.uploadSvcSet {
		svc, err := c.uploadSvc, c.uploadSvcErr
		c.mu.Unlock()
		return svc, err
	}
	c.mu.Unlock()

	svc, err := c.discoverUploadService(ctx)

	// A timeout is transient - the service may just have been slow to answer
	// this once. Only cache a definitive "not advertised" result, so a slow
	// disco walk doesn't permanently disable uploads for the rest of the
	// session.
	if !errors.Is(err, context.DeadlineExceeded) {
		c.mu.Lock()
		c.uploadSvc, c.uploadSvcErr, c.uploadSvcSet = svc, err, true
		c.mu.Unlock()
	}

	return svc, err
}

func (c *Client) discoverUploadService(ctx context.Context) (jid.JID, error) {
	root := c.JID.Domain()
	// XEP-0030 advertises HTTP-upload components as items of the account's
	// domain. Query items first: asking the domain for its own info before
	// this is unnecessary and some otherwise-working servers don't answer
	// that query promptly, making attachment sends appear stuck.
	iter := disco.FetchItems(ctx, items.Item{JID: root}, c.session)
	var services []items.Item
	for iter.Next() {
		services = append(services, iter.Item())
	}
	if err := iter.Err(); err != nil {
		_ = iter.Close()
		return jid.JID{}, fmt.Errorf("discovering upload service: %w", err)
	}
	// A disco item iterator holds the session response open. Mellium requires
	// it to be closed before starting another IQ request on that session.
	if err := iter.Close(); err != nil {
		return jid.JID{}, fmt.Errorf("closing upload-service discovery: %w", err)
	}

	// Query each item's features in turn. A single unresponsive item (a
	// gateway or component that never answers) must not be able to burn the
	// whole shared ctx before we ever reach the real upload service further
	// down the list - so each query gets its own bounded slice of time,
	// independent of how much the earlier items used.
	for i, item := range services {
		itemCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		info, err := disco.GetInfo(itemCtx, item.Node, item.JID, c.session)
		cancel()
		if err != nil {
			slog.Warn("upload discovery: failed to get info", "service", item.JID, "err", err)
			if ctx.Err() != nil {
				return jid.JID{}, fmt.Errorf("upload discovery timed out on %s (checked %d/%d services): %w", root, i, len(services), ctx.Err())
			}
			continue
		}
		var features []string
		for _, f := range info.Features {
			features = append(features, f.Var)
		}
		slog.Debug("upload discovery: service features", "service", item.JID, "features", features)
		if supportsUpload(info) {
			return item.JID, nil
		}
	}
	return jid.JID{}, fmt.Errorf("no XEP-0363 HTTP upload service advertised by %s", root)
}

func supportsUpload(info disco.Info) bool {
	for _, feature := range info.Features {
		if feature.Var == upload.NS {
			return true
		}
	}
	return false
}
