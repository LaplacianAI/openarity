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
	Model              ModelRef
	Pattern            PatternName
	System             []Content
	Tools              []Tool
	Skills             []Skill
	MaxSteps           int
	SteerContinuations int
	OutputSchema       *OutputSchema
	Parser             bool
	ParserModel        ModelRef
	Session            Session
	// Compaction keeps a long run's context small; nil leaves it off.
	Compaction *Compaction
	// Discovery holds tools found by search rather than sent up front; nil
	// sends every tool.
	Discovery *Discovery
}

func System(prompt string) []Content {
	return []Content{{Type: ContentText, Text: prompt, Cacheable: true}}
}
