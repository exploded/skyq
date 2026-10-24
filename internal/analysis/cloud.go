package analysis

// CloudSource says where a night's cloud verdict came from. It is stored
// as nights.cloud_source.
type CloudSource string

const (
	// CloudSkycam is skycam's sky API: cloud from each all-sky frame's star
	// count, read only while the roof is open and the sun is below −12°.
	CloudSkycam CloudSource = "skycam"
	// CloudLuminance is skyq's retired still-luminance detector. Every night
	// ingested before skycam carries it, and a rerun keeps it (SPEC §4.4).
	CloudLuminance CloudSource = "luminance"
	// CloudUnknown means no sky data: no onset, nothing attributed to cloud,
	// and the night is not clear-sky history. Unknown is never clear.
	CloudUnknown CloudSource = "unknown"
)
