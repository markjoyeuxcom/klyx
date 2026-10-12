package fleet

// CapReprobeEvery exposes the capability re-probe cadence seam to the
// external parity gate (parity_lab_test.go): a cadence-only lab override,
// the production detector unchanged (slice 6 spec §3).
var CapReprobeEvery = &capReprobeEvery
