package node

import "github.com/nickstrad/decentralized-object-store/internal/storage"

func SetCanaryNoDiscardRecount(v bool) { canaryNoDiscardRecount = v }
func SetCanaryIgnoreTombstones(v bool) { canaryIgnoreTombstones = v }

type CollectRecord = collectRecord

func ManifestOfRecord(b []byte) (*storage.Manifest, error) {
	r, err := decodeObject(b)
	if err != nil {
		return nil, err
	}
	return r.Manifest, nil
}

func SetCanaryUntrackedGoroutine(release chan struct{}) { canaryUntrackedGoroutine = release }
