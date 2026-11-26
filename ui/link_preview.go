package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/net/html"
)

// Link previews: a card under a message with a link - the page's site,
// title, description and picture (OpenGraph, else <title> and the
// description meta), or the picture itself when the link is to an image.
//
// Off by default (DisplayOptions.LinkPreviews): fetching a link tells
// whoever runs it, at this IP, that the message was read - and the link
// is the sender's choice, so neither are any addresses on this machine
// or its network reachable through it (see publicOnlyDial).

var linkPreviewsOn atomic.Bool

func setLinkPreviewsEnabled(on bool) { linkPreviewsOn.Store(on) }

// linkPagePreview is what a link's fetch found; failed when nothing worth
// a card.
type linkPagePreview struct {
	site, title, description string
	thumb                    mediaThumb
	failed                   bool
}

// linkPreviews holds fetched previews by URL. Like mediaThumbs: written
// only from Update (applyLinkPreview), so a row's height can't change
// between a full render and a splice of it.
var (
	linkPreviewMu      sync.Mutex
	linkPreviews       = map[string]linkPagePreview{}
	linkPreviewPending = map[string]bool{}
	linkPreviewQueue   []string
)

// lookupLinkPreview returns link's preview, queueing a fetch when there's
// none yet.
func lookupLinkPreview(link string) (linkPagePreview, bool) {
	linkPreviewMu.Lock()
	defer linkPreviewMu.Unlock()
	if p, ok := linkPreviews[link]; ok {
		return p, true
	}
	if !linkPreviewPending[link] {
		linkPreviewPending[link] = true
		linkPreviewQueue = append(linkPreviewQueue, link)
	}
	return linkPagePreview{}, false
}

func takeLinkPreviewRequests() []tea.Cmd {
	linkPreviewMu.Lock()
	queue := linkPreviewQueue
	linkPreviewQueue = nil
	linkPreviewMu.Unlock()
	cmds := make([]tea.Cmd, len(queue))
	for i, link := range queue {
		cmds[i] = fetchLinkPreviewCmd(link)
	}
	return cmds
}

type linkPreviewMsg struct {
	link    string
	preview linkPagePreview
}

// applyLinkPreview stores a finished fetch; true when there's a card to
// redraw for.
func applyLinkPreview(msg linkPreviewMsg) bool {
	linkPreviewMu.Lock()
	defer linkPreviewMu.Unlock()
	delete(linkPreviewPending, msg.link)
	linkPreviews[msg.link] = msg.preview
	return !msg.preview.failed
}

// linkPreviewSlots bounds concurrent fetches.
var linkPreviewSlots = make(chan struct{}, 2)

const (
	// linkPageMaxBytes is as much of a page as is read: the metadata is
	// in its head.
	linkPageMaxBytes = 512 << 10
	// linkImageMaxBytes caps the picture a card shows.
	linkImageMaxBytes = 5 << 20
	linkFetchTimeout  = 10 * time.Second
)

// allowLoopbackLinks lets tests reach an httptest server; see
// publicOnlyDial.
var allowLoopbackLinks bool

// publicOnlyDial refuses every address that isn't a public unicast one,
// checked on the address actually dialed - after DNS, and for every
// redirect - so a link can't make kage request anything on this machine
// or its network.
func publicOnlyDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("link preview: unresolved address %s", address)
	}
	if ip.IsLoopback() && allowLoopbackLinks {
		return nil
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return fmt.Errorf("link preview: %s is not a public address", ip)
	}
	return nil
}

var linkPreviewClient = &http.Client{
	Timeout: linkFetchTimeout,
	Transport: &http.Transport{
		// No proxy: it would be the one dialed, not the link's host.
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: linkFetchTimeout, Control: publicOnlyDial}).DialContext,
		TLSHandshakeTimeout: linkFetchTimeout,
		MaxIdleConns:        4,
		IdleConnTimeout:     30 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("redirect to %s", req.URL.Scheme)
		}
		return nil
	},
}

func fetchLinkPreviewCmd(link string) tea.Cmd {
	return func() tea.Msg {
		linkPreviewSlots <- struct{}{}
		defer func() { <-linkPreviewSlots }()
		p, err := fetchLinkPreview(link)
		if err != nil {
			return linkPreviewMsg{link: link, preview: linkPagePreview{failed: true}}
		}
		return linkPreviewMsg{link: link, preview: p}
	}
}

// linkGet fetches link, at most maxBytes of it, returning its media type.
func linkGet(link string, maxBytes int64, accept string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), linkFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "kage (link preview)")
	resp, err := linkPreviewClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes && !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return nil, "", fmt.Errorf("%s is too large", humanFileSize(resp.ContentLength))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, "", err
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return data, mediaType, nil
}

// fetchLinkPreview fetches link's page, and the picture it names.
func fetchLinkPreview(link string) (linkPagePreview, error) {
	base, err := url.Parse(link)
	if err != nil {
		return linkPagePreview{}, err
	}
	data, mediaType, err := linkGet(link, linkPageMaxBytes, "text/html,image/*;q=0.9")
	if err != nil {
		return linkPagePreview{}, err
	}
	if strings.HasPrefix(mediaType, "image/") {
		// Read in full only if it fit linkPageMaxBytes; a bigger one is
		// fetched again with the image cap.
		if len(data) >= linkPageMaxBytes {
			if data, _, err = linkGet(link, linkImageMaxBytes, "image/*"); err != nil {
				return linkPagePreview{}, err
			}
		}
		thumb, err := decodeMediaBytes(data)
		if err != nil {
			return linkPagePreview{}, err
		}
		return linkPagePreview{thumb: thumb}, nil
	}
	if mediaType != "text/html" && mediaType != "application/xhtml+xml" {
		return linkPagePreview{}, fmt.Errorf("no preview for %s", mediaType)
	}
	meta := parsePageMeta(data)
	p := linkPagePreview{site: meta.site, title: meta.title, description: meta.description}
	if p.site == "" {
		p.site = strings.TrimPrefix(base.Hostname(), "www.")
	}
	if meta.image != "" {
		if ref, err := base.Parse(meta.image); err == nil && (ref.Scheme == "http" || ref.Scheme == "https") {
			if img, _, err := linkGet(ref.String(), linkImageMaxBytes, "image/*"); err == nil {
				p.thumb, _ = decodeMediaBytes(img)
			}
		}
	}
	if p.title == "" && p.description == "" && p.thumb.img == nil {
		return linkPagePreview{}, errors.New("nothing to preview")
	}
	return p, nil
}

// decodeMediaBytes is decodeMediaThumb for data in memory.
func decodeMediaBytes(data []byte) (mediaThumb, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return mediaThumb{}, err
	}
	if px := int64(cfg.Width) * int64(cfg.Height); px <= 0 || px > avatarPreviewMaxPixels {
		return mediaThumb{}, fmt.Errorf("%d×%d is too large to preview", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return mediaThumb{}, err
	}
	return mediaThumb{img: downscale(img, mediaThumbEdge()), width: cfg.Width, height: cfg.Height}, nil
}

type pageMeta struct{ site, title, description, image string }

// parsePageMeta reads a page's OpenGraph metadata, falling back to its
// <title>, description meta and twitter:image. Stops at <body>: the head
// is where all of it lives.
func parsePageMeta(page []byte) pageMeta {
	var m pageMeta
	var docTitle, description, twitterImage string
	z := html.NewTokenizer(bytes.NewReader(page))
	inTitle := false
	for {
		switch z.Next() {
		case html.ErrorToken:
			return m.withFallbacks(docTitle, description, twitterImage)
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch string(name) {
			case "body":
				return m.withFallbacks(docTitle, description, twitterImage)
			case "title":
				inTitle = true
			case "meta":
				var key, content string
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					switch strings.ToLower(string(k)) {
					case "property", "name":
						key = strings.ToLower(string(v))
					case "content":
						content = strings.TrimSpace(string(v))
					}
				}
				switch key {
				case "og:title":
					m.title = content
				case "og:description":
					m.description = content
				case "og:image", "og:image:url", "og:image:secure_url":
					if m.image == "" {
						m.image = content
					}
				case "og:site_name":
					m.site = content
				case "description":
					description = content
				case "twitter:image", "twitter:image:src":
					twitterImage = content
				}
			}
		case html.TextToken:
			if inTitle && docTitle == "" {
				docTitle = strings.TrimSpace(string(z.Text()))
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); string(name) == "title" {
				inTitle = false
			}
		}
	}
}

func (m pageMeta) withFallbacks(title, description, image string) pageMeta {
	if m.title == "" {
		m.title = title
	}
	if m.description == "" {
		m.description = description
	}
	if m.image == "" {
		m.image = image
	}
	m.title = strings.Join(strings.Fields(m.title), " ")
	m.description = strings.Join(strings.Fields(m.description), " ")
	return m
}

// firstPreviewLink is the link in body a card is made for: the first
// http(s) one. aesgcm:// links are attachments, previewed as those.
func firstPreviewLink(body string) string {
	for _, link := range urlPattern.FindAllString(body, -1) {
		if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
			return link
		}
	}
	return ""
}

// linkCardDescriptionRows caps the description: a card is a hint of the
// page, not the page.
const linkCardDescriptionRows = 2

// renderLinkPreview is the card for body's first link at most width wide,
// nil when there's none (yet).
func (m Model) renderLinkPreview(body string, width int) []string {
	if !linkPreviewsOn.Load() || width < 8 {
		return nil
	}
	link := firstPreviewLink(body)
	if link == "" {
		return nil
	}
	p, ok := lookupLinkPreview(link)
	if !ok || p.failed {
		return nil
	}
	bar := m.styles.messageReply.Render("▎") + " "
	inner := width - 2
	var lines []string
	if p.title != "" || p.site != "" {
		head := p.title
		if p.site != "" && p.title != "" {
			head = p.site + " · " + p.title
		} else if p.site != "" {
			head = p.site
		}
		lines = append(lines, bar+m.styles.plainText.Bold(true).Render(ansi.Truncate(head, inner, "…")))
	}
	if p.description != "" {
		desc := strings.Split(ansi.Wrap(p.description, inner, " "), "\n")
		if len(desc) > linkCardDescriptionRows {
			desc = desc[:linkCardDescriptionRows]
			desc[len(desc)-1] = ansi.Truncate(desc[len(desc)-1]+" …", inner, "…")
		}
		for _, d := range desc {
			lines = append(lines, bar+m.styles.messageReply.Render(d))
		}
	}
	if p.thumb.img != nil {
		if block := renderThumbBlock("link:"+link, p.thumb, inner); block != "" {
			for _, row := range strings.Split(block, "\n") {
				lines = append(lines, bar+row)
			}
		}
	}
	return lines
}
