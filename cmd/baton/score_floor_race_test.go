//go:build race

package main

// replayFloorFactor drops to one under the race detector, and the reason is the
// measurement rather than the bound.
//
// The detector instruments every lock and map write the replay makes, and the
// replay is one of each per record: the same thirty-thousand-record store that
// boots in ~85 ms takes ~825 ms under it, a factor of ten. The daemon a timeout
// protects is not race-instrumented, so multiplying an instrumented boot by ten
// again would hold the production constant to a budget no production binary ever
// spends — and 10 s would start failing on machines where it is perfectly sound.
//
// So this run asserts the weaker true thing: a realistic store's boot, even
// instrumented, still fits inside the bound. The order of magnitude is asserted
// by the plain `go test` run, which is where the number's own units live.
const replayFloorFactor = 1
