package signals

var (
	goExt     = []string{".go"}
	rustExt   = []string{".rs"}
	pyExt     = []string{".py"}
	jsExt     = []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"}
	anyCodeEx = []string{".go", ".rs", ".py", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs",
		".java", ".kt", ".rb", ".cs", ".swift", ".php", ".scala", ".ex", ".exs"}
)

// q is a quoted string in any of the languages' quotes, captured.
const q = "[\"'`]([^\"'`]+)[\"'`]"

// builtin is the detectors Check ships. Each is written for the way its
// library is called, not for every way it could be; a repository whose
// telemetry these miss declares a detector of its own.
var builtin = []Detector{
	// Spans.
	{Name: "otel-go-span", Kind: Span, Extensions: goExt,
		Pattern: `\.Start\(\s*\w+,\s*"([^"]+)"`},
	{Name: "otel-py-span", Kind: Span, Extensions: pyExt,
		Pattern: `start_(?:as_current_)?span\(\s*` + q},
	{Name: "otel-js-span", Kind: Span, Extensions: jsExt,
		Pattern: `start(?:Active)?Span\(\s*` + q},
	{Name: "tracing-span", Kind: Span, Extensions: rustExt,
		Pattern: `\b(?:(?:trace|debug|info|warn|error)_span!|span!\(\s*(?:tracing::)?Level::\w+\s*,)\s*\(?\s*"([^"]+)"`},
	{Name: "tracing-instrument", Kind: Span, Extensions: rustExt,
		Pattern: `#\[(?:tracing::)?instrument\b(?:[^\]]*?name\s*=\s*"([^"]+)")?`},

	// Metrics.
	{Name: "otel-metric", Kind: Metric, Extensions: anyCodeEx,
		Pattern: `\.(?:(?:Int64|Float64)\w*(?:Counter|Histogram|Gauge)|create_(?:counter|histogram|up_down_counter|gauge|observable_\w+)|create(?:Counter|Histogram|UpDownCounter|Gauge|Observable\w+))\(\s*(?:name\s*=\s*)?` + q},
	{Name: "prometheus-go", Kind: Metric, Extensions: goExt,
		Pattern: `prometheus\.\w*Opts\{[\s\S]{0,300}?Name:\s*"([^"]+)"`},
	{Name: "prometheus-py", Kind: Metric, Extensions: pyExt,
		Pattern: `\b(?:Counter|Gauge|Histogram|Summary|Info|Enum)\(\s*` + q},
	{Name: "prom-client", Kind: Metric, Extensions: jsExt,
		Pattern: `new\s+(?:\w+\.)?(?:Counter|Gauge|Histogram|Summary)\(\s*\{[\s\S]{0,200}?name:\s*` + q},
	{Name: "metrics-rs", Kind: Metric, Extensions: rustExt,
		Pattern: `\b(?:counter|gauge|histogram|describe_counter|describe_gauge|describe_histogram)!\(\s*"([^"]+)"`},
	{Name: "prometheus-rs", Kind: Metric, Extensions: rustExt,
		Pattern: `\b(?:register_\w+!\(|Opts::new\(|HistogramOpts::new\()\s*"([^"]+)"`},

	// Log lines.
	{Name: "go-log", Kind: Log, Extensions: goExt,
		Pattern: `\b(?:slog|\w*[lL]og(?:ger)?)\.(?:Debug|Info|Warn|Error|Fatal|Panic|Print)[a-z]*(?:Context)?\(\s*(?:ctx,\s*)?"([^"]+)"`},
	{Name: "rust-log", Kind: Log, Extensions: rustExt,
		Pattern: `\b(?:trace|debug|info|warn|error)!\((?:[^;]*?,\s*)??"([^"]*)"\s*[,)]`},
	{Name: "python-logging", Kind: Log, Extensions: pyExt,
		Pattern: `\b(?:_?log(?:ger)?|logging|LOG|LOGGER)\.(?:debug|info|warning|warn|error|exception|critical)\(\s*[fFrR]?` + q},
	{Name: "js-log", Kind: Log, Extensions: jsExt,
		Pattern: `\b(?:logger|log|console)\.(?:trace|debug|info|warn|error|fatal|log)\(\s*(?:\{[^}]*\},\s*)?` + q},

	// Events and errors.
	{Name: "span-event", Kind: Event, Extensions: anyCodeEx,
		Pattern: `\.(?:AddEvent|add_event|addEvent)\(\s*` + q},
	{Name: "span-error", Kind: Error, Extensions: anyCodeEx,
		Pattern: `\.(?:RecordError|record_exception|recordException|record_error)\(()`},
	{Name: "sentry", Kind: Error, Extensions: anyCodeEx,
		Pattern: `\b(?:Sentry\.capture(?:Exception|Message)|sentry\.Capture(?:Exception|Message)|sentry_sdk\.capture_(?:exception|message)|sentry::capture_(?:error|message|anyhow))\(()`},

	// Feature flags.
	{Name: "openfeature", Kind: Flag, Extensions: anyCodeEx,
		Pattern: `\.(?:(?:Get)?(?:Boolean|String|Integer|Int|Float|Number|Object)(?:Value|Details)|get_(?:boolean|string|integer|float|object)_(?:value|details))\(\s*(?:ctx,\s*)?` + q},
	{Name: "launchdarkly", Kind: Flag, Extensions: anyCodeEx,
		Pattern: `\.(?:BoolVariation|StringVariation|IntVariation|Float64Variation|JSONVariation|variation|boolVariation|stringVariation|bool_variation|string_variation)\(\s*` + q},
}

// Builtin is a fresh copy of the detectors Check ships, compiled.
func Builtin() []Detector {
	out := make([]Detector, len(builtin))
	copy(out, builtin)
	for i := range out {
		if err := out[i].compile(); err != nil {
			panic("signals: built-in detector " + out[i].Name + ": " + err.Error())
		}
	}
	return out
}
