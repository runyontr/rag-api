package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/sashabaranov/go-openai"

	wclient "github.com/weaviate/weaviate-go-client/v4/weaviate"
)

func main() {
	// turn the chat instory into one thing to query weaviate.  Or do we just use the last conversation message?
	config := wclient.Config{
		Scheme: getEnv("WEAVIATE_SCHEME", "https"),
		Host:   getEnv("WEAVIATE_HOST", "weaviate.leapfrogai.bigbang.dev"),
	}
	weaviateClient, err := wclient.NewClient(config)
	if err != nil {
		fmt.Printf("Error occurred %v", err)
		return
	}
	openaiConfig := openai.DefaultConfig("FAKE_TOKEN")
	openaiConfig.BaseURL = getEnv("OPENAI_API_URL", "https://leapfrogai.leapfrogai.bigbang.dev/openai/v1")
	openaiClient := openai.NewClientWithConfig(openaiConfig)
	rag := RAGHander{
		WeaviateClient: weaviateClient,
		OpenAIClient:   openaiClient,
	}
	count := getEnv("WEAVIATE_QUERY_COUNT", "2")
	cint, err := strconv.Atoi(count)
	if err != nil {
		fmt.Printf("Error parsing WEAVIATE_QUERY_COUNT env variable ( %v ) into int: %v\n", count, err)
	} else {
		rag.WeaviateQueryCount = cint
	}
	threshold := getEnv("WEAVIATE_SCORE_THRESHOLD", "5")
	cfloat, err := strconv.ParseFloat(threshold, 64)
	if err != nil {
		fmt.Printf("Error parsing WEAVIATE_SCORE_THRESHOLD env variable ( %v ) into int: %v\n", threshold, err)
	} else {
		rag.WeaviateScoreThreshold = cfloat
	}

	input := openai.ChatCompletionRequest{
		Model: "mpt-30b-chat-ggml",
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleSystem,
				Content: "You are a helpful AI bot",
			},
			{
				Role:    openai.ChatMessageRoleUser,
				Content: "Tell me a story abotu cats",
			},
		},
		MaxTokens:   2000,
		Temperature: .7,
		Stream:      true,
	}
	stream, err := rag.OpenAIClient.CreateChatCompletionStream(context.Background(), input)
	if err != nil {
		fmt.Printf("Error creating completionstream: %v\n", err)
	}
	for {
		cResp, err := stream.Recv()
		if err == io.EOF {
			// OpenAI places a space in between the data key and payload in HTTP. So, I guess we're bug-for-bug compatible.
			res, err := json.Marshal(cResp)
			if err != nil {
				fmt.Printf("chat: Error marshalling chat completion message: %v\n", err)
				break
			}
			fmt.Printf("Recieved message from backend server:\n\n\n%v\n\n", string(res))
			break
		} else if err != nil {
			fmt.Printf("Recieved nonEOF error: %v\n", err)

		}
		// OpenAI places a space in between the data key and payload in HTTP. So, I guess we're bug-for-bug compatible.
		res, err := json.Marshal(cResp)
		if err != nil {
			fmt.Printf("chat: Error marshalling chat completion message: %v\n", err)
			break
		}
		fmt.Printf("Recieved message from backend server:\n\n\n%v\n\n", string(res))
	}
}
