package storage

func PlantSkipChecksum() func() {
	plantSkipChecksum = true
	return func() { plantSkipChecksum = false }
}
