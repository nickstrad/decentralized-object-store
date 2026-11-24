package telemetry

import (
	"encoding/json"
	"time"
)

// Add one constructor entry alongside each new adapter implementation. No init hooks.
var factories = map[string]Factory{"otel": newOTel, "prometheus": newPrometheus, "csv": newCSV}

var settingValidators = map[string]func(time.Duration, json.RawMessage) error{"prometheus": validatePrometheusSettings}
