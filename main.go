package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	speech "cloud.google.com/go/speech/apiv2"
	"cloud.google.com/go/speech/apiv2/speechpb"
	"cloud.google.com/go/storage"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/api/option"
)

type UserTranscription struct {
	Nickname string
	Filename string
}

type Line struct {
	Nickname string
	Words    []Word
}

func (l *Line) StartTime() time.Duration {
	return l.Words[0].StartTime
}

func (l *Line) EndTime() time.Duration {
	return l.Words[len(l.Words)-1].StartTime
}

func (l *Line) String() string {
	return fmt.Sprintf("%s: %s", l.Nickname, l.WordsString())
}

func (l *Line) WordsString() string {
	wordStrings := make([]string, len(l.Words))
	for i, word := range l.Words {
		wordStrings[i] = word.Text
	}
	return strings.Join(wordStrings, " ")
}

type Word struct {
	Nickname  string
	Text      string
	StartTime time.Duration
}

func (w *Word) String() string {
	return w.Text
}

//go:embed bucketname
var bucketName string

//go:embed recognizer
var recognizer string

//go:embed openai.token
var openAiToken string

//go:embed assistant.id
var openAiAssistantId string

func main() {
	requests := []UserTranscription{
		{
			Nickname: "Hero",
			Filename: "input/user1.flac",
		},
		{
			Nickname: "GameMaster",
			Filename: "input/user2.flac",
		},
	}
	allWords := make([]Word, 0)
	transcriptionChan := make(chan []Word, len(requests))
	for _, r := range requests {
		go func(req UserTranscription, c chan<- []Word) {
			gcsUri, err := uploadFileToGCS(bucketName, "audio-files/"+req.Nickname+filepath.Ext(req.Filename), req.Filename)
			if err != nil {
				slog.Error("could not upload file to GCS", "file", req.Filename, "error", err)
				c <- make([]Word, 0)
				return
			}
			words, err := transcribeFileGc(gcsUri, req.Nickname)
			if err != nil {
				slog.Error("could not transcribe file", "file", req.Filename, "error", err)
				c <- make([]Word, 0)
				return
			}
			c <- words
			/*
				words, err := transcribeFileOpenAI(req.Filename, req.Nickname)
				if err != nil {
					slog.Error("could not transcribe file", "file", req.Filename, "error", err)
					c <- make([]Word, 0)
					return
				}
				c <- words
			*/
		}(r, transcriptionChan)
	}
	for i := 0; i < len(requests); i++ {
		allWords = append(allWords, <-transcriptionChan...)
	}
	slices.SortFunc(allWords, func(a, b Word) int {
		if a.StartTime < b.StartTime {
			return -1
		} else if a.StartTime > b.StartTime {
			return 1
		}
		return 0
	})
	lines := constructLines(allWords)
	fullConversation := ""
	for i, line := range lines {
		if i == 0 {
			fullConversation = line.String()
			continue
		}
		fullConversation = fmt.Sprintf("%s\n%s", fullConversation, line.String())
	}
	fmt.Printf("This is the full conversation:\n\n%s\n", fullConversation)
	if err := os.WriteFile("transcription.txt", []byte(fullConversation), 0600); err != nil {
		slog.Warn("could not store transcription", "error", err)
	}
	if len(fullConversation) == 0 {
		slog.Error("No coversation detected, nothing to summarize...")
		os.Exit(1)
	}
	fmt.Println("\n\nTrying summary now...")
	summary, err := summarize(lines)
	if err != nil {
		slog.Error("could not create summary", "error", err)
		os.Exit(1)
	}
	fmt.Printf("This is the summary:\n\n%s\n", summary)
	if err := os.WriteFile("summary.txt", []byte(summary), 0600); err != nil {
		slog.Warn("could not store summary", "error", err)
	}
}

/*
func transcribeFileOpenAI(file, nickname string) ([]Word, error) {
	client := openai.NewClient(openAiToken)
	resp, err := client.CreateTranscription(context.Background(), openai.AudioRequest{
		Model:                  "whisper-1",
		FilePath:               file,
		Format:                 openai.AudioResponseFormatVerboseJSON,
		TimestampGranularities: []string{"word"},
	})
	if err != nil {
		return nil, err
	}
	words := make([]Word, 0)
	for _, segment := range resp.Segments {
		currentTimestamp := segment.Start
		wordSplit := strings.Split(segment.Text, " ")
		stepSize := (segment.End - segment.Start) / float64(len(wordSplit))
		for _, word := range wordSplit {
			words = append(words, Word{
				Nickname:  nickname,
				Text:      word,
				StartTime: time.Duration(currentTimestamp),
			})
			currentTimestamp += stepSize
		}
	}
	return words, nil
}
*/

func transcribeFileGc(gcsUri, nickname string) ([]Word, error) {
	ctx := context.Background()
	client, err := speech.NewClient(ctx, option.WithEndpoint("europe-west3-speech.googleapis.com:443"))
	if err != nil {
		return nil, errors.Join(err, errors.New("failed to create client"))
	}
	defer client.Close()
	rec, err := client.GetRecognizer(ctx, &speechpb.GetRecognizerRequest{
		Name: recognizer,
	})
	if err != nil {
		return nil, errors.Join(err, errors.New("failed to get regonizer"))
	}
	op, err := client.BatchRecognize(ctx, &speechpb.BatchRecognizeRequest{
		Recognizer: recognizer,
		Config:     rec.DefaultRecognitionConfig,
		RecognitionOutputConfig: &speechpb.RecognitionOutputConfig{
			Output: &speechpb.RecognitionOutputConfig_GcsOutputConfig{
				GcsOutputConfig: &speechpb.GcsOutputConfig{
					Uri: strings.TrimSuffix(gcsUri, filepath.Ext(gcsUri)) + ".json",
				},
			},
		},
		Files: []*speechpb.BatchRecognizeFileMetadata{
			{
				AudioSource: &speechpb.BatchRecognizeFileMetadata_Uri{
					Uri: gcsUri,
				},
			},
		},
	})
	if err != nil {
		return nil, errors.Join(err, errors.New("could not start batch recognition"))
	}

	resp, err := op.Wait(ctx)
	if err != nil {
		return nil, errors.Join(err, errors.New("failed to recognize"))
	}

	words := make([]Word, 0)
	storageClient, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	defer storageClient.Close()
	for _, result := range resp.Results {
		if result.Error != nil {
			return nil, fmt.Errorf("could not transcribe. %s - status %d details %v", result.Error.Message, result.Error.Code, result.Error.Details)
		}
		obj := storageClient.Bucket(bucketName).Object(strings.TrimPrefix(result.GetCloudStorageResult().Uri, "gs://"+bucketName+"/"))
		reader, err := obj.NewReader(ctx)
		if err != nil {
			return nil, err
		}
		defer reader.Close()

		var recognition StoredRecognitionResult
		if err := json.NewDecoder(reader).Decode(&recognition); err != nil {
			return nil, errors.Join(errors.New("could not parse recognition result"), err)
		}
		for _, r := range recognition.Results {
			for _, word := range r.Alternatives[0].Words {
				words = append(words, Word{
					Nickname:  nickname,
					Text:      word.Word,
					StartTime: time.Duration(word.StartOffset),
				})
			}
		}
	}
	return words, nil
}

type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case float64:
		*d = Duration(time.Duration(value))
		return nil
	case string:
		tmp, err := time.ParseDuration(value)
		if err != nil {
			return err
		}
		*d = Duration(tmp)
		return nil
	default:
		return errors.New("invalid duration")
	}
}

type StoredRecognitionResult struct {
	Results []struct {
		Alternatives []struct {
			Transcript string  `json:"transcript"`
			Confidence float64 `json:"confidence"`
			Words      []struct {
				Word        string   `json:"word"`
				StartOffset Duration `json:"startOffset"`
				EndOffset   Duration `json:"endOffset"`
			} `json:"words"`
		} `json:"alternatives"`
		ResultEndOffset Duration `json:"resultEndOffset"`
		LanguageCode    string   `json:"languageCode"`
	} `json:"results"`
}

func uploadFileToGCS(bucketName, fileName, filePath string) (gcsUri string, err error) {
	gcsUri = fmt.Sprintf("gs://%s/%s", bucketName, fileName)
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()

	bucket := client.Bucket(bucketName)
	object := bucket.Object(fileName)

	// Check if object already present
	if _, e := object.Attrs(ctx); e == nil || !errors.Is(e, storage.ErrObjectNotExist) {
		return
	}

	writer := object.NewWriter(ctx)
	defer writer.Close()

	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if _, err = io.Copy(writer, file); err != nil {
		return "", err
	}

	return
}

func constructLines(words []Word) []Line {
	if len(words) == 0 {
		return nil
	}
	lines := make([]Line, 0)
	continousWords := make([]Word, 1)
	lastWord := words[0]
	continousWords[0] = lastWord
	for i, currentWord := range words {
		if i == 0 {
			continue
		}
		if currentWord.Text == lastWord.Text && currentWord.StartTime == lastWord.StartTime {
			// Protect from word doubling.
			// Don't ask me why this happens. The issue is most certainly somewhere else but I was too lazy to find it...
			continue
		}
		if lastWord.Nickname == currentWord.Nickname && (currentWord.StartTime-lastWord.StartTime) < 7*time.Second {
			// Word is in streak
			continousWords = append(continousWords, currentWord)
			lastWord = currentWord
			continue
		}
		// current word is not in streak. Form new line
		lines = append(lines, asLine(continousWords))
		continousWords = make([]Word, 1)
		continousWords[0] = currentWord
		lastWord = currentWord
	}
	lines = append(lines, asLine(continousWords))
	return lines
}

func asLine(words []Word) Line {
	return Line{
		Nickname: words[0].Nickname,
		Words:    words,
	}
}

func summarize(lines []Line) (string, error) {
	client := openai.NewClient(openAiToken)
	ctx := context.Background()
	linesPerRequest := make([]string, 0)
	currentText := ""
	tokenCount := float32(0)
	for _, line := range lines {
		lineTokens := float32(len(line.Words))*1.5 + 4 // rough and conservative estimation. See https://help.openai.com/en/articles/4936856-what-are-tokens-and-how-to-count-them
		newTotal := tokenCount + lineTokens
		if newTotal > 122000 { // GPT-4-turbo has a limit of 128.000 tokens. Adjust this for any other model
			currentText += "\nNEXT CHUNK AFTER RESPONSE"
			linesPerRequest = append(linesPerRequest, currentText)
			currentText = line.String()
			tokenCount = lineTokens
		} else {
			currentText += "\n" + line.String()
			tokenCount = newTotal
		}
	}
	linesPerRequest = append(linesPerRequest, currentText)
	resp, err := client.CreateThreadAndRun(ctx, openai.CreateThreadAndRunRequest{
		Thread: openai.ThreadRequest{
			Messages: []openai.ThreadMessage{
				{
					Role:    "user",
					Content: linesPerRequest[0],
				},
			},
		},
		RunRequest: openai.RunRequest{
			AssistantID: openAiAssistantId,
		},
	})
	if err != nil {
		return "", err
	}
	runID := resp.ID
	for i, text := range linesPerRequest {
		if i == 0 {
			continue
		}
		if err := waitForRun(ctx, client, resp.ThreadID, runID); err != nil {
			return "", err
		}
		msg, err := client.CreateMessage(ctx, resp.ThreadID, openai.MessageRequest{
			Role:    "user",
			Content: text,
		})
		if err != nil {
			return "", err
		}
		runID = *msg.RunID
	}
	if err := waitForRun(ctx, client, resp.ThreadID, runID); err != nil {
		return "", err
	}
	messages, err := client.ListMessage(ctx, resp.ThreadID, nil, nil, nil, nil)
	if err != nil {
		return "", err
	}
	if delResp, err := client.DeleteThread(ctx, resp.ThreadID); err != nil || !delResp.Deleted {
		slog.Warn("could not delete thread after finish", "threadID", resp.ThreadID, "response", delResp, "error", err)
	}
	return messages.Messages[0].Content[0].Text.Value, nil
}

func waitForRun(ctx context.Context, client *openai.Client, threadID, runID string) error {
loop:
	for {
		run, err := client.RetrieveRun(ctx, threadID, runID)
		if err != nil {
			return err
		}
		switch run.Status {
		case "queued", "in_progress", "cancelling":
			time.Sleep(3 * time.Second)
			continue loop
		case "completed":
			break loop
		default:
			return fmt.Errorf("summarization run exited with status %q", run.Status)
		}
	}
	return nil
}
