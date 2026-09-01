package agent

type LoopType string

const (
	LoopReAct  LoopType = "react"
	LoopCode   LoopType = "code"
	LoopCustom LoopType = "custom"
)

type ModelRef struct {
	Name        string
	MaxTokens   int
	Temperature *float64
}

type Spec struct {
	Model    ModelRef
	Loop     LoopType
	System   []Content
	Tools    []Tool
	Skills   []Skill
	MaxSteps int
}

func System(prompt string) []Content {
	return []Content{{Type: ContentText, Text: prompt, Cacheable: true}}
}
