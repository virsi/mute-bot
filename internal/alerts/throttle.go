package alerts

// Throttle re-exports the Throttler interface this package depends on so
// that the cmd/processor wiring can name the dependency without crossing
// the storage/redis import boundary. The redis-backed AlertThrottle in
// internal/storage/redis already satisfies this contract.
type Throttle = Throttler
