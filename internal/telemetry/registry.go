package telemetry

// Add one constructor entry alongside each new adapter implementation. No init hooks.
var factories = map[string]Factory{"otel": newOTel}
