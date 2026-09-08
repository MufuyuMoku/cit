package vault

import "path/filepath"

// Blobs are addressed by their hex SHA-256 and sharded two levels deep on the
// first four hex digits: 256 directories, each holding 256 more. At a million
// chunks that is about sixteen files per leaf directory, which keeps every
// directory listing cheap on every filesystem CIT targets.
const shardDepth = 2

// blobRoot is the directory holding every chunk blob.
func (v *Vault) blobRoot() string {
	return filepath.Join(v.root, "blobs")
}

// stageRoot is where a Store writes new blobs before it commits. Anything left
// here belongs to a Store that did not finish.
func (v *Vault) stageRoot() string {
	return filepath.Join(v.root, "staging")
}

// blobPath returns the on-disk location of the chunk with the given hex hash.
func (v *Vault) blobPath(chunkHash string) string {
	parts := make([]string, 0, shardDepth+2)
	parts = append(parts, v.blobRoot())
	for i := 0; i < shardDepth; i++ {
		parts = append(parts, chunkHash[i*2:i*2+2])
	}
	parts = append(parts, chunkHash)
	return filepath.Join(parts...)
}
