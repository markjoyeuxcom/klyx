// Package promqlproof proves, with the Prometheus parser, that every
// registered metrics template carries the reader's scope in every vector
// selector (Settings S3b §3.3). It is test-only and a module of its own: the
// parser never enters Klyx's build.
package promqlproof
