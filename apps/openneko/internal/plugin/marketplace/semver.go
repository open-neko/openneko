package marketplace

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Compare implements the TS semverCompare semantics: numeric major/minor/patch,
// then prerelease-presence-aware comparison (no prerelease beats a prerelease),
// then prerelease lexicographic.
func Compare(a, b string) int {
	pa := mustParse(a)
	pb := mustParse(b)
	if d := pa.major - pb.major; d != 0 {
		return d
	}
	if d := pa.minor - pb.minor; d != 0 {
		return d
	}
	if d := pa.patch - pb.patch; d != 0 {
		return d
	}
	switch {
	case pa.pre == "" && pb.pre != "":
		return 1
	case pa.pre != "" && pb.pre == "":
		return -1
	case pa.pre != "" && pb.pre != "":
		return strings.Compare(pa.pre, pb.pre)
	}
	return 0
}

type parsed struct {
	major, minor, patch int
	pre                 string
}

var semverRX = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-(.+))?$`)

func mustParse(v string) parsed {
	m := semverRX.FindStringSubmatch(v)
	if m == nil {
		panic(fmt.Sprintf("invalid semver: %s", v))
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return parsed{major: major, minor: minor, patch: patch, pre: m[4]}
}
