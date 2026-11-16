package meter

import "encoding/json"

// Usage is the quantity block of one response.
type Usage struct {
	PromptTokens, CachedTokens, CacheWriteTokens, CompletionTokens, ReasoningTokens int64
	ToolCalls                                                                       int
	ResponseModel                                                                   string
	Complete                                                                        bool // false when no usage block was seen
}

// usageBlock is the "usage" object of an OpenAI chat.completion body.
type usageBlock struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens     int64 `json:"cached_tokens"`
		CacheWriteTokens int64 `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// ParseJSON reads usage from an unstreamed chat.completion body. A body that is not JSON, or
// that has no "usage" object, gives a Usage whose Complete is false.
func ParseJSON(body []byte) Usage {
	var doc struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage *usageBlock `json:"usage"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Usage == nil {
		return Usage{}
	}
	u := Usage{
		PromptTokens:     doc.Usage.PromptTokens,
		CachedTokens:     doc.Usage.PromptTokensDetails.CachedTokens,
		CacheWriteTokens: doc.Usage.PromptTokensDetails.CacheWriteTokens,
		CompletionTokens: doc.Usage.CompletionTokens,
		ReasoningTokens:  doc.Usage.CompletionTokensDetails.ReasoningTokens,
		ResponseModel:    doc.Model,
		Complete:         true,
	}
	for _, c := range doc.Choices {
		u.ToolCalls += len(c.Message.ToolCalls)
	}
	return u
}
