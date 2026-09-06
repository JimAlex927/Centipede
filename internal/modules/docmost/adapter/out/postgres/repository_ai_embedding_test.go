package postgres

import "testing"

func TestAIEmbeddingChunksAreUnicodeSafe(t *testing.T) {
	chunks := AIEmbeddingChunks("你好世界abcdef", 4, 1)
	if len(chunks) != 3 || chunks[0] != "你好世界" || chunks[1] != "界abc" || chunks[2] != "cdef" {
		t.Fatalf("unexpected chunks: %#v", chunks)
	}
}

func TestCosineSimilarity(t *testing.T) {
	if got := cosineSimilarity([]float32{1, 0}, []float32{1, 0}); got != 1 {
		t.Fatalf("identical vectors score = %v, want 1", got)
	}
	if got := cosineSimilarity([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Fatalf("orthogonal vectors score = %v, want 0", got)
	}
	if got := cosineSimilarity([]float32{1}, []float32{1, 0}); got != 0 {
		t.Fatalf("different dimensions score = %v, want 0", got)
	}
}
