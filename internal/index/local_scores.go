package index

import (
	"context"
	"errors"
	"math"
)

// LocalSimilarityScores compares query and document embeddings on loopback.
// Results are ordered by query then document. This does not open or modify
// either search index.
func LocalSimilarityScores(ctx context.Context, name string, queries, documents []string) ([][]float64, error) {
	e, err := localEmbedder()
	if err != nil {
		return nil, err
	}
	defer e.client.CloseIdleConnections()
	model, err := e.model(ctx, name)
	if err != nil {
		return nil, err
	}
	dimension := 0
	embed := func(texts []string, query bool) ([][]float32, error) {
		vectors := make([][]float32, 0, len(texts))
		for start := 0; start < len(texts); start += embeddingBatch {
			batch, err := e.vectors(ctx, model, texts[start:min(start+embeddingBatch, len(texts))], query)
			if err != nil {
				return nil, err
			}
			for _, vector := range batch {
				if dimension == 0 {
					dimension = len(vector)
				}
				if len(vector) != dimension {
					return nil, errors.New("Ollama returned inconsistent embedding dimensions")
				}
			}
			vectors = append(vectors, batch...)
		}
		return vectors, nil
	}

	documentVectors, err := embed(documents, false)
	if err != nil {
		return nil, err
	}
	queryVectors, err := embed(queries, true)
	if err != nil {
		return nil, err
	}
	current, err := e.model(ctx, model.name)
	if err != nil {
		return nil, err
	}
	if current != model {
		return nil, errors.New("Ollama model changed during similarity scoring; retry")
	}
	scores := make([][]float64, len(queryVectors))
	for i, query := range queryVectors {
		scores[i] = make([]float64, len(documentVectors))
		for j, document := range documentVectors {
			var dot float64
			for k, value := range query {
				dot += float64(value) * float64(document[k])
			}
			scores[i][j] = math.Max(-1, math.Min(1, dot))
		}
	}
	return scores, nil
}
