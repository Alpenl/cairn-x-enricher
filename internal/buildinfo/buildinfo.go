package buildinfo

// Version is the semantic release version injected by the build.
var Version = "dev"

// Commit is the source revision injected by the build.
var Commit = "none"

// Date is the release build time injected by the build.
var Date = "unknown"

// Info describes the source revision used to build the executable.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Current returns the build metadata embedded in this executable.
func Current() Info {
	return Info{
		Version: Version,
		Commit:  Commit,
		Date:    Date,
	}
}
