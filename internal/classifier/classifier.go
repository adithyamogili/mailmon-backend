package classifier

import "context"

type EmailInput struct {
	Subject     string
	From        string
	BodyPreview string
}

type Classification struct {
	Relevant bool   `json:"relevant"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
}

type Classifier interface {
	ClassifyBatch(ctx context.Context, inputs []EmailInput) ([]Classification, error)
}
