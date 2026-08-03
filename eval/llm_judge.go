/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package eval

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/schema"
)

var _ Evaluator = (*LLMJudgeEval)(nil)

// LLMJudgeEval uses an LLM as a judge to evaluate agent output quality.
type LLMJudgeEval struct {
	caller largemodel.Caller
	model  string
}

// NewLLMJudgeEval creates a new LLMJudgeEval with the given model caller and
// model name. Returns an error if caller is nil or model is empty.
func NewLLMJudgeEval(caller largemodel.Caller, model string) (*LLMJudgeEval, error) {
	if caller == nil {
		return nil, errors.New("LLMJudgeEval requires a non-nil Caller")
	}

	if model == "" {
		return nil, errors.New("LLMJudgeEval requires a non-empty model name")
	}

	return &LLMJudgeEval{
		caller: caller,
		model:  model,
	}, nil
}

// Evaluate implements Evaluator.
func (e *LLMJudgeEval) Evaluate(ctx context.Context, c *EvalCase) (*EvalResult, error) {
	start := time.Now()

	if c.Actual == nil {
		return nil, errors.New("LLM judge eval requires a non-nil Actual response")
	}

	prompt := e.buildJudgePrompt(c)

	req := &largemodel.Request{
		Model: e.model,
		Messages: []schema.Message{
			schema.NewUserMessage(e.caller.Protocol(), prompt),
		},
	}

	resp, err := e.caller.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("LLM judge call failed: %w", err)
	}

	responseText := resp.Message.Text()

	score, passed, reasoning, parseErr := parseJudgeResponse(responseText)

	duration := time.Since(start).Milliseconds()
	usage := &resp.Usage

	if parseErr != nil {
		return &EvalResult{
			CaseID: c.ID,
			Score:  0,
			Passed: false,
			Details: []EvalDetail{
				{
					Name:    "llm_judge",
					Score:   0,
					Passed:  false,
					Message: fmt.Sprintf("failed to parse judge response: %v", parseErr),
				},
			},
			Duration: duration,
			Usage:    usage,
		}, nil
	}

	return &EvalResult{
		CaseID: c.ID,
		Score:  score,
		Passed: passed,
		Details: []EvalDetail{
			{
				Name:    "llm_judge",
				Score:   score,
				Passed:  passed,
				Message: reasoning,
			},
		},
		Duration: duration,
		Usage:    usage,
	}, nil
}

// buildJudgePrompt constructs the evaluation prompt for the LLM judge.
func (e *LLMJudgeEval) buildJudgePrompt(c *EvalCase) string {
	var b strings.Builder

	b.WriteString("You are an evaluation judge. Score the following agent output.\n\n")

	inputText := ""
	if c.Input != nil && len(c.Input.Messages) > 0 {
		last := c.Input.Messages[len(c.Input.Messages)-1]
		inputText = last.Text()
	}

	fmt.Fprintf(&b, "Input: %s\n", inputText)
	fmt.Fprintf(&b, "Actual Output: %s\n", lastAssistantText(c.Actual))

	if c.Expected != nil {
		fmt.Fprintf(&b, "Expected Output: %s\n", lastAssistantText(c.Expected))
	}

	if len(c.Criteria) > 0 {
		fmt.Fprintf(&b, "Evaluation Criteria: %s\n", strings.Join(c.Criteria, ", "))
	}

	b.WriteString("\nRespond in exactly this format:\n")
	b.WriteString("SCORE: <a number between 0.0 and 1.0>\n")
	b.WriteString("PASSED: <true or false>\n")
	b.WriteString("REASONING: <your explanation>\n")

	return b.String()
}

// normalizeLabel strips markdown formatting and normalizes a label prefix
// for robust parsing of LLM judge responses.
// It handles variations like "**SCORE:**", "Score:", "SCORE :", etc.
func normalizeLabel(line string) string {
	// Remove markdown bold markers.
	line = strings.ReplaceAll(line, "**", "")
	line = strings.ReplaceAll(line, "*", "")
	line = strings.TrimSpace(line)

	return line
}

// cutLabel checks if a line starts with any variation of the given label
// (case-insensitive) and returns the value after the colon.
func cutLabel(line, label string) (string, bool) {
	normalized := normalizeLabel(line)
	upper := strings.ToUpper(normalized)
	prefix := strings.ToUpper(label) + ":"

	if strings.HasPrefix(upper, prefix) {
		value := normalized[len(prefix):]

		return strings.TrimSpace(value), true
	}

	return "", false
}

// parseJudgeResponse parses the LLM judge response to extract score, passed, and reasoning.
// It is tolerant of case variations, markdown formatting, and multi-line reasoning.
func parseJudgeResponse(text string) (score float64, passed bool, reasoning string, err error) {
	lines := strings.Split(text, "\n")

	var scoreFound, passedFound, reasoningFound bool

	for i, line := range lines {
		line = strings.TrimSpace(line)

		if after, ok := cutLabel(line, "SCORE"); ok {
			score, err = strconv.ParseFloat(after, 64)
			if err != nil {
				return 0, false, "", fmt.Errorf("invalid score value %q: %w", after, err)
			}

			score = clamp(score, 0, 1)
			scoreFound = true
		} else if after, ok := cutLabel(line, "PASSED"); ok {
			passed, err = strconv.ParseBool(after)
			if err != nil {
				return 0, false, "", fmt.Errorf("invalid passed value %q: %w", after, err)
			}

			passedFound = true
		} else if after, ok := cutLabel(line, "REASONING"); ok {
			// Collect multi-line reasoning: everything from this line to the end.
			var parts []string

			parts = append(parts, after)

			for j := i + 1; j < len(lines); j++ {
				trimmed := strings.TrimSpace(lines[j])
				if trimmed == "" {
					continue
				}

				// Stop if we encounter another label.
				if _, ok := cutLabel(trimmed, "SCORE"); ok {
					break
				}

				if _, ok := cutLabel(trimmed, "PASSED"); ok {
					break
				}

				parts = append(parts, trimmed)
			}

			reasoning = strings.Join(parts, " ")
			reasoningFound = true
		}

		if scoreFound && passedFound && reasoningFound {
			break
		}
	}

	if !scoreFound {
		return 0, false, "", errors.New("SCORE not found in judge response")
	}

	if !passedFound {
		return 0, false, "", errors.New("PASSED not found in judge response")
	}

	return score, passed, reasoning, nil
}
