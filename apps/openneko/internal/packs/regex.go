package packs

import "regexp"

var (
	coreInt   = regexp.MustCompile(`^[-+]?[0-9]+$`)
	octalInt  = regexp.MustCompile(`^[0-7]+$`)
	hexInt    = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	coreFloat = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?$`)

	PackIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)
	keyPattern     = regexp.MustCompile(`^[a-z][a-z0-9_.-]+$`)
	hostPattern    = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	appPathPattern = regexp.MustCompile(`^/[a-zA-Z0-9/_-]*$`)
	artifactKeyRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_.-]*$`)
	targetRefRe    = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
	gqlNameRe      = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)
	headerNameRe   = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	fieldPathRe    = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+)*$`)
	bodyFieldRe    = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	zipPartRe      = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)
