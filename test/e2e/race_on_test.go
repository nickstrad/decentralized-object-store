//go:build race

package e2e

// raceEnabled reports that tests were built with -race so tier S sweeps trim
// redundant single-threaded seed volume.
const raceEnabled = true
