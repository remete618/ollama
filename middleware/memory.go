package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
)

type memorySearchRequest struct {
	Query  string `json:"query"`
	UserID string `json:"user_id"`
	TopK   int    `json:"top_k"`
}

type memorySearchResponse struct {
	Memories []struct {
		Content string `json:"content"`
	} `json:"memories"`
}

type memoryAddRequest struct {
	Text   string `json:"text"`
	UserID string `json:"user_id"`
}

func MemoryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		memoryURL := os.Getenv("OLLAMA_MEMORY_URL")
		if memoryURL == "" {
			c.Next()
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Next()
			return
		}
		c.Request.Body.Close()

		var req api.ChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			c.Next()
			return
		}

		var lastUserMsg string
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				lastUserMsg = req.Messages[i].Content
				break
			}
		}

		if lastUserMsg == "" {
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			c.Next()
			return
		}

		memories := searchMemories(memoryURL, lastUserMsg)
		if len(memories) > 0 {
			var facts []string
			for _, m := range memories {
				facts = append(facts, "- "+m)
			}
			memMsg := api.Message{
				Role:    "system",
				Content: fmt.Sprintf("[Memory Context] The following facts are known about this user:\n%s", strings.Join(facts, "\n")),
			}
			req.Messages = append([]api.Message{memMsg}, req.Messages...)

			var b bytes.Buffer
			if err := json.NewEncoder(&b).Encode(req); err != nil {
				c.Request.Body = io.NopCloser(bytes.NewReader(body))
				c.Next()
				return
			}
			c.Request.Body = io.NopCloser(&b)
		} else {
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}

		c.Next()

		go addMemory(memoryURL, lastUserMsg)
	}
}

func searchMemories(baseURL, query string) []string {
	client := &http.Client{Timeout: 2 * time.Second}

	searchReq := memorySearchRequest{
		Query:  query,
		UserID: "default",
		TopK:   5,
	}

	body, err := json.Marshal(searchReq)
	if err != nil {
		slog.Warn("memory middleware: failed to marshal search request", "error", err)
		return nil
	}

	resp, err := client.Post(baseURL+"/search", "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("memory middleware: sidecar unreachable", "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("memory middleware: search returned non-200", "status", resp.StatusCode)
		return nil
	}

	var searchResp memorySearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		slog.Warn("memory middleware: failed to decode search response", "error", err)
		return nil
	}

	var results []string
	for _, m := range searchResp.Memories {
		if m.Content != "" {
			results = append(results, m.Content)
		}
	}
	return results
}

func addMemory(baseURL, text string) {
	client := &http.Client{Timeout: 30 * time.Second}

	addReq := memoryAddRequest{
		Text:   text,
		UserID: "default",
	}

	body, err := json.Marshal(addReq)
	if err != nil {
		slog.Warn("memory middleware: failed to marshal add request", "error", err)
		return
	}

	resp, err := client.Post(baseURL+"/add", "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("memory middleware: failed to store memory", "error", err)
		return
	}
	defer resp.Body.Close()
}
