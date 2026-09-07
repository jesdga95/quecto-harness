package llm

type ModelStatus string

const (
	StatusUnloaded    ModelStatus = "unloaded"
	StatusLoading     ModelStatus = "loading"
	StatusLoaded      ModelStatus = "loaded"
	StatusSleeping    ModelStatus = "sleeping"
	StatusDownloading ModelStatus = "downloading"
)

// Model is one entry of the model list, reduced to what the picker needs.
// CtxHint is a per-slot context size and CtxSource says how it was obtained.
type Model struct {
	ID         string
	Status     ModelStatus
	Failed     bool
	ExitCode   int
	Modalities []string
	CtxHint    int
	CtxSource  string
}
