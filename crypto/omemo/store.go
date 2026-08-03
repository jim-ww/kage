// Package omemo implements omemo.Store (github.com/jim-ww/omemo-go) backed
// by kage's shared sqlite database, one Store per account (accountJID is
// baked in at construction, not passed per-call — the upstream Store
// interface is single-device/single-account by design).
package omemo

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"

	omemolib "github.com/jim-ww/omemo-go"

	"github.com/jim-ww/kage/storage"
)

// Store implements omemo.Store for one account, against the shared
// storage.Queries database.
type Store struct {
	db         *storage.Queries
	accountJID string
}

// NewStore returns a Store scoped to accountJID.
func NewStore(db *storage.Queries, accountJID string) *Store {
	return &Store{db: db, accountJID: accountJID}
}

var _ omemolib.Store = (*Store)(nil)

func (s *Store) IdentityKeyPair(ctx context.Context) (ed25519.PrivateKey, error) {
	row, err := s.db.GetOmemoIdentity(ctx, s.accountJID)
	if err != nil {
		return nil, err
	}
	return ed25519.PrivateKey(row.Privatekey), nil
}

func (s *Store) SetIdentityKeyPair(ctx context.Context, priv ed25519.PrivateKey) error {
	existingDeviceID, err := s.currentDeviceID(ctx)
	if err != nil {
		return err
	}
	return s.db.SetOmemoIdentity(ctx, storage.SetOmemoIdentityParams{
		AccountJid: s.accountJID,
		PrivateKey: priv,
		DeviceID:   existingDeviceID,
	})
}

// currentDeviceID returns the device ID already stored for this account, or
// 0 if none is stored yet — SetLocalDevice is expected to fill it in
// separately (InitIdentity calls SetIdentityKeyPair before SetLocalDevice).
func (s *Store) currentDeviceID(ctx context.Context) (int64, error) {
	row, err := s.db.GetOmemoIdentity(ctx, s.accountJID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return row.Deviceid, nil
}

func (s *Store) LocalDevice(ctx context.Context) (omemolib.Device, error) {
	row, err := s.db.GetOmemoIdentity(ctx, s.accountJID)
	if err != nil {
		return omemolib.Device{}, err
	}
	return omemolib.Device{JID: s.accountJID, ID: omemolib.DeviceID(row.Deviceid)}, nil
}

func (s *Store) SetLocalDevice(ctx context.Context, dev omemolib.Device) error {
	priv, err := s.IdentityKeyPair(ctx)
	if err != nil {
		return err
	}
	return s.db.SetOmemoIdentity(ctx, storage.SetOmemoIdentityParams{
		AccountJid: s.accountJID,
		PrivateKey: priv,
		DeviceID:   int64(dev.ID),
	})
}

func (s *Store) CurrentSignedPreKey(ctx context.Context) (omemolib.SignedPreKeyRecord, error) {
	row, err := s.db.GetOmemoCurrentSignedPreKey(ctx, s.accountJID)
	if err != nil {
		return omemolib.SignedPreKeyRecord{}, err
	}
	return omemolib.SignedPreKeyRecord{
		ID:        uint32(row.ID),
		Public:    row.Public,
		Private:   row.Private,
		Signature: row.Signature,
	}, nil
}

func (s *Store) StaleSignedPreKey(ctx context.Context) (omemolib.SignedPreKeyRecord, bool, error) {
	row, err := s.db.GetOmemoStaleSignedPreKey(ctx, s.accountJID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return omemolib.SignedPreKeyRecord{}, false, nil
		}
		return omemolib.SignedPreKeyRecord{}, false, err
	}
	return omemolib.SignedPreKeyRecord{
		ID:        uint32(row.ID),
		Public:    row.Public,
		Private:   row.Private,
		Signature: row.Signature,
	}, true, nil
}

func (s *Store) RotateSignedPreKey(ctx context.Context, next omemolib.SignedPreKeyRecord) error {
	if err := s.db.DeleteOmemoStaleSignedPreKey(ctx, s.accountJID); err != nil {
		return err
	}
	if err := s.db.MarkOmemoSignedPreKeyStale(ctx, s.accountJID); err != nil {
		return err
	}
	return s.db.InsertOmemoSignedPreKey(ctx, storage.InsertOmemoSignedPreKeyParams{
		AccountJid: s.accountJID,
		ID:         int64(next.ID),
		Public:     next.Public,
		Private:    next.Private,
		Signature:  next.Signature,
	})
}

func (s *Store) PreKeyCount(ctx context.Context) (int, error) {
	n, err := s.db.CountOmemoPreKeys(ctx, s.accountJID)
	return int(n), err
}

func (s *Store) PreKeys(ctx context.Context) ([]omemolib.PreKeyRecord, error) {
	rows, err := s.db.ListOmemoPreKeys(ctx, s.accountJID)
	if err != nil {
		return nil, err
	}
	recs := make([]omemolib.PreKeyRecord, len(rows))
	for i, r := range rows {
		recs[i] = omemolib.PreKeyRecord{ID: uint32(r.ID), Public: r.Public, Private: r.Private}
	}
	return recs, nil
}

func (s *Store) NextPreKeyID(ctx context.Context) (uint32, error) {
	row, err := s.db.GetOmemoNextPreKeyID(ctx, s.accountJID)
	next := int64(1)
	if err == nil {
		next = row
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err := s.db.SetOmemoNextPreKeyID(ctx, storage.SetOmemoNextPreKeyIDParams{
		AccountJid: s.accountJID,
		NextID:     next + 1,
	}); err != nil {
		return 0, err
	}
	return uint32(next), nil
}

func (s *Store) PutPreKeys(ctx context.Context, recs []omemolib.PreKeyRecord) error {
	for _, r := range recs {
		if err := s.db.InsertOmemoPreKey(ctx, storage.InsertOmemoPreKeyParams{
			AccountJid: s.accountJID,
			ID:         int64(r.ID),
			Public:     r.Public,
			Private:    r.Private,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ConsumePreKey(ctx context.Context, id uint32) (omemolib.PreKeyRecord, error) {
	row, err := s.db.ConsumeOmemoPreKey(ctx, storage.ConsumeOmemoPreKeyParams{
		AccountJid: s.accountJID,
		ID:         int64(id),
	})
	if err != nil {
		return omemolib.PreKeyRecord{}, fmt.Errorf("consume prekey %d: %w", id, err)
	}
	return omemolib.PreKeyRecord{ID: uint32(row.ID), Public: row.Public, Private: row.Private}, nil
}

func (s *Store) Session(ctx context.Context, dev omemolib.Device) ([]byte, bool, error) {
	data, err := s.db.GetOmemoSession(ctx, storage.GetOmemoSessionParams{
		AccountJid: s.accountJID,
		PeerJid:    dev.JID,
		DeviceID:   int64(dev.ID),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

func (s *Store) PutSession(ctx context.Context, dev omemolib.Device, data []byte) error {
	return s.db.PutOmemoSession(ctx, storage.PutOmemoSessionParams{
		AccountJid: s.accountJID,
		PeerJid:    dev.JID,
		DeviceID:   int64(dev.ID),
		Data:       data,
	})
}

func (s *Store) DeleteSession(ctx context.Context, dev omemolib.Device) error {
	return s.db.DeleteOmemoSession(ctx, storage.DeleteOmemoSessionParams{
		AccountJid: s.accountJID,
		PeerJid:    dev.JID,
		DeviceID:   int64(dev.ID),
	})
}

func (s *Store) Trust(ctx context.Context, identityKey ed25519.PublicKey) (omemolib.TrustState, error) {
	state, err := s.db.GetOmemoTrust(ctx, storage.GetOmemoTrustParams{
		AccountJid:  s.accountJID,
		IdentityKey: identityKey,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return omemolib.TrustUndecided, nil
		}
		return omemolib.TrustUndecided, err
	}
	return omemolib.TrustState(state), nil
}

func (s *Store) SetTrust(ctx context.Context, identityKey ed25519.PublicKey, state omemolib.TrustState) error {
	return s.db.SetOmemoTrust(ctx, storage.SetOmemoTrustParams{
		AccountJid:  s.accountJID,
		IdentityKey: identityKey,
		State:       int64(state),
	})
}

func (s *Store) Devices(ctx context.Context, jid string) ([]omemolib.DeviceID, error) {
	rows, err := s.db.ListOmemoDevices(ctx, storage.ListOmemoDevicesParams{
		AccountJid: s.accountJID,
		PeerJid:    jid,
	})
	if err != nil {
		return nil, err
	}
	ids := make([]omemolib.DeviceID, len(rows))
	for i, r := range rows {
		ids[i] = omemolib.DeviceID(r)
	}
	return ids, nil
}

func (s *Store) SetDevices(ctx context.Context, jid string, devices []omemolib.DeviceID) error {
	if err := s.db.DeleteOmemoDevices(ctx, storage.DeleteOmemoDevicesParams{
		AccountJid: s.accountJID,
		PeerJid:    jid,
	}); err != nil {
		return err
	}
	for _, id := range devices {
		if err := s.db.InsertOmemoDevice(ctx, storage.InsertOmemoDeviceParams{
			AccountJid: s.accountJID,
			PeerJid:    jid,
			DeviceID:   int64(id),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RemoteIdentityKey(ctx context.Context, dev omemolib.Device) (ed25519.PublicKey, bool, error) {
	key, err := s.db.GetOmemoRemoteIdentity(ctx, storage.GetOmemoRemoteIdentityParams{
		AccountJid: s.accountJID,
		PeerJid:    dev.JID,
		DeviceID:   int64(dev.ID),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return ed25519.PublicKey(key), true, nil
}

func (s *Store) PutRemoteIdentityKey(ctx context.Context, dev omemolib.Device, key ed25519.PublicKey) error {
	return s.db.PutOmemoRemoteIdentity(ctx, storage.PutOmemoRemoteIdentityParams{
		AccountJid:  s.accountJID,
		PeerJid:     dev.JID,
		DeviceID:    int64(dev.ID),
		IdentityKey: key,
	})
}
