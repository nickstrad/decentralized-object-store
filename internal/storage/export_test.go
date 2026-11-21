package storage

// PlantSkipChecksum disables SHA-256 verification in DecodeShard for canary
// tests and returns a restore function that re-enables it.
func PlantSkipChecksum() func() {
	plantSkipChecksum = true
	return func() { plantSkipChecksum = false }
}
