package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"kubeagent/internal/core"
)

// Compatible implements the Chat Completions function-calling wire protocol.
// Credentials are server-only. No model-generated argument becomes a shell command.
type Compatible struct {
	Knowledge *core.KnowledgeIndex
	BaseURL   string
	Model     string
	APIKey    string
	Client    *http.Client
}

func NewCompatible(baseURL, model, apiKey string) (*Compatible, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("AGENT_LLM_BASE_URL 必须是有效的 HTTP(S) API 基地址，不含凭证或查询参数")
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !local {
		return nil, errors.New("远程模型接口必须使用 HTTPS")
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("请配置 AGENT_LLM_MODEL")
	}
	if apiKey == "" && !local {
		return nil, errors.New("远程模型接口需要 AGENT_LLM_API_KEY")
	}
	return &Compatible{BaseURL: u.String(), Model: model, APIKey: apiKey}, nil
}
func (m *Compatible) Name() string { return "compatible:" + m.Model }
func (m *Compatible) Next(ctx context.Context, input core.AgentInput) (core.AgentDecision, error) {
	task, _ := json.Marshal(map[string]any{"task": input.Request, "incident": map[string]any{"id": input.Incident.ID, "type": input.Incident.Type, "title": input.Incident.Title, "target": input.Incident.Target}, "availableEvidence": len(input.Incident.Evidence)})
	messages := []map[string]any{{"role": "system", "content": core.AgentSystemPrompt}, {"role": "user", "content": string(task)}}
	for _, exchange := range input.Exchanges {
		d := exchange.Decision
		assistant := map[string]any{"role": "assistant", "content": "", "tool_calls": []map[string]any{{"id": d.ID, "type": "function", "function": map[string]string{"name": d.Tool, "arguments": string(d.Arguments)}}}}
		if d.Reasoning != "" {
			assistant["reasoning_content"] = d.Reasoning
		}
		messages = append(messages, assistant, map[string]any{"role": "tool", "tool_call_id": d.ID, "content": string(exchange.Output)})
	}
	response, err := m.chat(ctx, map[string]any{"model": m.Model, "stream": false, "messages": messages, "tools": core.AgentToolDefinitions(), "tool_choice": "auto"})
	if err != nil {
		return core.AgentDecision{}, err
	}
	if len(response.Choices) != 1 || response.Choices[0].FinishReason != "tool_calls" || len(response.Choices[0].Message.ToolCalls) != 1 {
		return core.AgentDecision{}, errors.New("模型没有返回单个完整工具调用")
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if call.Type != "function" || call.ID == "" || len(call.ID) > 200 || len(call.Function.Name) > 100 || len(call.Function.Arguments) > 16384 || !json.Valid([]byte(call.Function.Arguments)) {
		return core.AgentDecision{}, errors.New("模型工具调用参数无效")
	}
	return core.AgentDecision{ID: call.ID, Tool: call.Function.Name, Arguments: json.RawMessage(call.Function.Arguments), Reasoning: response.Choices[0].Message.Reasoning, InputTokens: response.Usage.Prompt, OutputTokens: response.Usage.Completion}, nil
}

type compatibleEnvelope struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning_content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
	} `json:"usage"`
}

func (m *Compatible) chat(ctx context.Context, body any) (compatibleEnvelope, error) {
	var result compatibleEnvelope
	// DeepSeek enables thinking by default and rejects named/required choices
	// in that mode. Keep reasoning enabled; callers still validate the exact
	// function, argument schema, evidence and plan binding before using a result.
	if u, e := url.Parse(m.BaseURL); e == nil && strings.EqualFold(u.Hostname(), "api.deepseek.com") {
		if original, ok := body.(map[string]any); ok {
			choice, exists := original["tool_choice"]
			mode, isString := choice.(string)
			if exists && (!isString || mode == "required") {
				adapted := make(map[string]any, len(original))
				for key, value := range original {
					adapted[key] = value
				}
				adapted["tool_choice"] = "auto"
				body = adapted
			}
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return result, err
	}
	endpoint := strings.TrimRight(m.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return result, errors.New("模型接口地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	if m.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.APIKey)
	}
	client := http.Client{Timeout: 2 * time.Minute}
	if m.Client != nil {
		client = *m.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errors.New("模型服务不可达或超时")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return result, modelHTTPError(res)
	}
	payload, err := io.ReadAll(io.LimitReader(res.Body, 1024*1024+1))
	if err != nil || len(payload) > 1024*1024 {
		return result, errors.New("模型响应读取失败或过大")
	}
	if json.Unmarshal(payload, &result) != nil {
		return result, errors.New("模型响应不是有效的 Chat Completions JSON")
	}
	return result, nil
}

// Map recognized failures to fixed messages. Never expose a provider's raw
// error body, which can echo an API key, request evidence or private logs.
func modelHTTPError(res *http.Response) error {
	hint := "请检查服务配置与额度"
	switch res.StatusCode {
	case 400, 422:
		hint = "模型拒绝请求参数，请核对模型名称与工具调用支持"
		var payload struct {
			Error struct {
				Message string `json:"message"`
				Param   string `json:"param"`
			} `json:"error"`
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, 16385))
		if err == nil && len(data) <= 16384 && json.Unmarshal(data, &payload) == nil {
			message := strings.ToLower(payload.Error.Message)
			switch {
			case strings.Contains(message, "tool_choice") && (strings.Contains(message, "thinking") || strings.Contains(message, "reasoning")):
				hint = "当前推理模式不支持强制工具选择，需要使用 auto 并校验工具结果"
			case payload.Error.Param == "model" || strings.Contains(message, "model") && (strings.Contains(message, "not found") || strings.Contains(message, "does not exist") || strings.Contains(message, "invalid")):
				hint = "模型名称无效或该账号无权访问，请使用服务商提供的模型 ID"
			case payload.Error.Param == "tools" || payload.Error.Param == "tool_choice":
				hint = "模型拒绝工具调用参数，请核对接口的工具调用能力"
			}
		}
	case 401, 403:
		hint = "API Key 无效或无权访问该模型"
	case 402:
		hint = "模型账号额度不足"
	case 404:
		hint = "接口地址或模型不存在，请核对 API 基地址与模型 ID"
	case 429:
		hint = "模型服务限流或配额不足，请稍后重试并检查配额"
	case 500, 502, 503, 504:
		hint = "模型服务暂时不可用，请稍后重试"
	}
	return fmt.Errorf("模型返回 HTTP %d；%s", res.StatusCode, hint)
}

// The existing single-incident Diagnose button shares the same provider.
// It reuses structured diagnosis and evidence validation; the multi-step loop
// lives in AgentRunner.
func (m *Compatible) Diagnose(ctx context.Context, incident core.Incident) (core.Diagnosis, error) {
	d, err := m.conclusion(ctx, incident, nil, nil)
	if err != nil {
		return core.RuleDiagnosis(incident), err
	}
	return d.Diagnosis, nil
}

func evidenceReferenceSchema(evidence []core.Evidence) (map[string]any, error) {
	ids := []string{}
	seen := map[string]bool{}
	for _, e := range evidence {
		if e.ID != "" && !seen[e.ID] {
			ids = append(ids, e.ID)
			seen[e.ID] = true
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("没有可引用的现场证据，停止模型诊断与复核")
	}
	return map[string]any{"type": "array", "minItems": 1, "uniqueItems": true, "description": "只能原样引用本次现场证据 ID；知识条目 ID 和资源 UID 不能作为证据引用", "items": map[string]any{"type": "string", "enum": ids}}, nil
}
