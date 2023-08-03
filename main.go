package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/penglongli/gin-metrics/ginmetrics"
	"github.com/sashabaranov/go-openai"

	wclient "github.com/weaviate/weaviate-go-client/v4/weaviate"
	"github.com/weaviate/weaviate-go-client/v4/weaviate/graphql"
)

func main() {
	// try out some weaviate things

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
	openaiConfig.HTTPClient.Timeout = 10 * time.Minute
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
	threshold := getEnv("WEAVIATE_SCORE_THRESHOLD", "3")
	cfloat, err := strconv.ParseFloat(threshold, 64)
	if err != nil {
		fmt.Printf("Error parsing WEAVIATE_SCORE_THRESHOLD env variable ( %v ) into int: %v\n", threshold, err)
	} else {
		rag.WeaviateScoreThreshold = cfloat
	}

	r := gin.Default()
	m := ginmetrics.GetMonitor()
	m.SetMetricPath("/metrics")

	m.Use(r)
	// :database parameter is not currently being used
	r.POST("/openai/:database/v1/chat/completions", rag.chat)
	// Models is required for the chatbot-ui to show what models are available
	r.GET("/openai/:database/v1/models", rag.models)
	r.GET("/healthz")

	r.Run()

	// metaGetter := c.Misc().MetaGetter()
	// meta, err := metaGetter.Do(context.Background())
	// if err != nil {
	// 	fmt.Printf("Error occurred %v", err)
	// 	return
	// }
	// fmt.Printf("Weaviate meta information\n")
	// fmt.Printf("hostname: %s version: %s\n", meta.Hostname, meta.Version)
	// fmt.Printf("enabled modules: %+v\n", meta.Modules)

	// rag.QueryWeaviate(context.Background(), "What is the MPT-7B-Instruct model built to do?")
}

/*
def query_weaviate(q, k=4, threshold=5):

	# query = "What LLM has the best performance for chat"


	bm25 = {
	"query": q,
	}

	result = (
	  client.query
	  .get("Slack", ["content","_additional {score} ", "source", "channel", "channel_id"])
	  .with_bm25(**bm25)
	  .with_limit(k)
	  .do()
	)


	return result
*/
func (rag *RAGHander) QueryWeaviate(ctx context.Context, prompt string) ([]SlackData, error) {
	contents := make([]SlackData, 0)
	bm25 := rag.WeaviateClient.GraphQL().Bm25ArgBuilder().
		WithQuery(prompt)

	request := rag.WeaviateClient.GraphQL().Get().
		WithClassName("Slack").
		WithFields(graphql.Field{Name: "content"}, graphql.Field{Name: "_additional {score}"}, graphql.Field{Name: "channel"}, graphql.Field{Name: "source"}).
		WithLimit(rag.WeaviateQueryCount).
		// WithNearVector(nearText)
		// WithBM25(rag.Client.GraphQL().Bm25ArgBuilder().WithQuery(prompt))
		WithBM25(bm25)

	// b, _ := json.MarshalIndent(request, "", "\t")
	// fmt.Println(string(b))

	responses, err := request.Do(ctx)
	// fmt.Printf("%v\n", responses)

	if err != nil {
		fmt.Printf("Error running query: %v\n", err)
		return contents, err
	}
	for _, v := range responses.Data {
		// obj := v.(map[string]interface{})
		// for _, s := range obj {
		// 	obj2 := s.([]interface{})
		// 	for _, v2 := range obj2 {
		// 		map2 := v2.(map[string]interface{})
		// 		fmt.Printf("Channel: %v\n", map2["channel"])
		// 		fmt.Printf("Source: %v\n", map2["source"])
		// 		fmt.Printf("Content: %v\n", map2["content"])
		// 		additionals := map2["_additional"].(map[string]interface{})
		// 		fmt.Printf("Score: %v\n", additionals["score"])
		// 	}
		// }
		// fmt.Printf("%v -> %v\n", k, v)
		j, _ := json.Marshal(v)
		sd := &Get{}
		json.Unmarshal(j, sd)
		contents = make([]SlackData, 0)
		fmt.Printf("Filtering weaviate responses.  Threshold is %v\n", rag.WeaviateScoreThreshold)
		for _, d := range sd.Slack {
			score, err := strconv.ParseFloat(d.Additional.Score, 64)
			if err != nil {
				continue
			}
			if score > rag.WeaviateScoreThreshold {
				contents = append(contents, d)
			} else {
				fmt.Printf("Skipping weaviate response with score %v\n", score)
			}
		}
		return contents, nil

	}
	return contents, fmt.Errorf("did not find anything in the weaviate database")
}

type SlackData struct {
	Additional struct {
		Score string `json:"score"`
	} `json:"_additional"`
	Channel string `json:"channel"`
	Content string `json:"content"`
	Source  string `json:"source"`
}

type Get struct {
	Slack []SlackData `json:"Slack"`
}

type RAGHander struct {
	WeaviateClient         *wclient.Client
	WeaviateQueryCount     int
	WeaviateScoreThreshold float64
	OpenAIClient           *openai.Client
}

func (rag *RAGHander) models(c *gin.Context) {
	models, e := rag.OpenAIClient.ListModels(context.Background())

	if e != nil {
		fmt.Printf("Error getting models: %v\n", e)
		c.JSON(500, e)
	} else {
		c.JSON(200, models)

	}

}

func (rag *RAGHander) chat(c *gin.Context) {
	// Bind JSON body to a struct with c.BindJSON()
	var input openai.ChatCompletionRequest
	if err := c.BindJSON(&input); err != nil {
		fmt.Printf("500: Error marshalling input to object: %v\n", err)
		// Handle error
		c.JSON(500, err)
		return
	}

	originalMessages := input.Messages

	// Just look at the last prompt to query weaviate.  COuld also look to add ALL the messages?
	data, err := rag.QueryWeaviate(context.Background(), originalMessages[len(originalMessages)-1].Content)
	if err != nil {
		fmt.Printf("Error querying Weaviate: %v\n", err)
		// what to do here?
		c.JSON(500, err)
		return
	}
	suffix := ""
	if len(data) > 0 {
		for _, d := range data {
			fmt.Printf("Weaviate context:\n%v\n", d)
			suffix += d.Source + "\n"
		}
		fmt.Printf("Suffix with links:\n%v\n", suffix)

		//Build a new set of query messages
		queryMessages := make([]openai.ChatCompletionMessage, len(originalMessages)+2)
		// overlaps except for the last entries
		for i := 0; i < len(originalMessages)-1; i++ {
			queryMessages[i] = originalMessages[i]
		}
		//build some pretend user prompt with the contents of weaviate
		//Could also look at the system prompt update with this data
		fakeUserMessage := "Hey, here are some excerpts of documentation that you can use to help answer my future questions:"
		for _, d := range data {
			fakeUserMessage += "\n\n"
			fakeUserMessage += d.Content
		}

		queryMessages[len(originalMessages)-1] = openai.ChatCompletionMessage{
			Role:         openai.ChatMessageRoleUser,
			Content:      fakeUserMessage,
			Name:         "",
			FunctionCall: &openai.FunctionCall{},
		}

		queryMessages[len(originalMessages)] = openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleAssistant,
			Content: "Thank you, I'll use that information, if its relevant, to help answer the following prompt",
		}
		queryMessages[len(originalMessages)+1] = originalMessages[len(originalMessages)-1]
		queryMessages[len(originalMessages)+1].Role = openai.ChatMessageRoleUser
		input.Messages = queryMessages

	}

	// looking to stream it back
	if input.Stream {
		fmt.Printf("chat: Streaming!")
		// id, _ := uuid.NewRandom()
		// // DEMO things
		// res, err := json.Marshal(openai.ChatCompletionStreamResponse{
		// 	ID:      id.String(),
		// 	Created: time.Now().Unix(),
		// 	Model:   input.Model,
		// 	Object:  "chat.completion",
		// 	Choices: []openai.ChatCompletionStreamChoice{
		// 		{
		// 			Delta: openai.ChatCompletionStreamChoiceDelta{
		// 				Role: openai.ChatMessageRoleAssistant,
		// 			},
		// 		},
		// 	},
		// })
		// c.SSEvent("", fmt.Sprintf(" %s", res))
		// res, err = json.Marshal(openai.ChatCompletionStreamResponse{
		// 	ID:      id.String(),
		// 	Created: time.Now().Unix(),
		// 	Model:   input.Model,
		// 	Object:  "chat.completion",
		// 	Choices: []openai.ChatCompletionStreamChoice{
		// 		{
		// 			Delta: openai.ChatCompletionStreamChoiceDelta{
		// 				Content: "That's a great question and this is your response",
		// 			},
		// 		},
		// 	},
		// })
		// c.SSEvent("", fmt.Sprintf(" %s", res))
		// res, err = json.Marshal(openai.ChatCompletionStreamResponse{
		// 	ID:      id.String(),
		// 	Created: time.Now().Unix(),
		// 	Model:   input.Model,
		// 	Object:  "chat.completion",
		// 	Choices: []openai.ChatCompletionStreamChoice{
		// 		{
		// 			FinishReason: openai.FinishReasonStop,
		// 		},
		// 	},
		// })
		// c.SSEvent("", fmt.Sprintf(" %s", res))
		// c.SSEvent("", " [DONE]")
		// return
		stream, err := rag.OpenAIClient.CreateChatCompletionStream(context.Background(), input)
		if err != nil {
			fmt.Printf("chat: Error Getting streaming chat completion message: %v\n", err)
			c.JSON(500, err)
		}
		for {
			cResp, err := stream.Recv()
			if err == io.EOF {
				fmt.Printf("Recieved EOF from backend server.  Sending Done\n")
				// OpenAI places a space in between the data key and payload in HTTP. So, I guess we're bug-for-bug compatible.
				// res, err := json.Marshal(cResp)
				// if err != nil {
				// 	fmt.Printf("chat: Error marshalling chat completion message: %v\n", err)
				// 	c.SSEvent("", " [DONE]")
				// 	c.JSON(500, err)
				// 	break
				// }
				// fmt.Printf("Recieved message from backend server:\n\n\n%v\n\n", string(res))
				// c.SSEvent("", fmt.Sprintf(" %s", res))
				c.SSEvent("", " [DONE]")
				break
			} else if err != nil {
				fmt.Printf("Recieved nonEOF error: %v\n", err)
				c.SSEvent("", " [DONE]")
				break
			}
			if cResp.ID == "" {
				// send done
				fmt.Printf("recieved empty object, so the stream is done.  Sending Links that were used.\n")
				id, _ := uuid.NewRandom()
				res, _ := json.Marshal(openai.ChatCompletionStreamResponse{
					ID:      id.String(),
					Created: time.Now().Unix(),
					Model:   input.Model,
					Object:  "chat.completion",
					Choices: []openai.ChatCompletionStreamChoice{
						{
							Delta: openai.ChatCompletionStreamChoiceDelta{
								Content: suffix,
							},
						},
					},
				})
				fmt.Printf("Sending Suffix with links:\n%v\n", suffix)
				c.SSEvent("", fmt.Sprintf(" %s", res))
				c.SSEvent("", " [DONE]")
				break
			}
			// OpenAI places a space in between the data key and payload in HTTP. So, I guess we're bug-for-bug compatible.
			res, err := json.Marshal(cResp)
			if err != nil {
				fmt.Printf("chat: Error marshalling chat completion message: %v\n", err)
				c.JSON(500, err)
				break
			}
			fmt.Printf("Recieved message from backend server:\n\n\n%v\n", string(res))

			c.SSEvent("", fmt.Sprintf(" %s", res))
			fmt.Printf("Sent\n")
		}

	} else {
		fmt.Printf("chat:")
		result, err := rag.OpenAIClient.CreateChatCompletion(context.Background(), input)
		if err != nil {
			fmt.Printf("chat: Error Getting chat completion message: %v\n", err)
			c.JSON(500, err)
			return
		}
		res, err := json.Marshal(result)
		if err != nil {
			fmt.Printf("chat: Error marshalling chat completion message: %v\n", err)
			c.JSON(500, err)
			return
		}
		c.JSON(200, res)

	}
}

// getEnv get key environment variable if exist otherwise return defalutValue
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if len(value) == 0 {
		return defaultValue
	}
	return value
}
