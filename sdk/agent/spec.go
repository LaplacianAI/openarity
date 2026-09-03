package agent

type PatternName string

const (
	PatternReAct      PatternName = "react"
	PatternPlan       PatternName = "plan"
	PatternReWOO      PatternName = "rewoo"
	PatternReflection PatternName = "reflection"
	PatternCode       PatternName = "code"
	PatternCustom     PatternName = "custom"
)

type ModelRef struct {
	Name        string
	MaxTokens   int
	Temperature *float64
}

type Spec struct {
	Model    ModelRef
	Pattern  PatternName
	System   []Content
	Tools    []Tool
	Skills   []Skill
	MaxSteps int
}

func System(prompt string) []Content {
	return []Content{{Type: ContentText, Text: prompt, Cacheable: true}}
}
