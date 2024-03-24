package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	speech "cloud.google.com/go/speech/apiv1"
	"cloud.google.com/go/speech/apiv1/speechpb"
	"cloud.google.com/go/storage"
	"github.com/sashabaranov/go-openai"
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
		{
			Nickname: "Adventurer2",
			Filename: "input/user3.flac",
		},
	}
	allWords := make([]Word, 0)
	transcriptionChan := make(chan []Word, len(requests))
	for _, r := range requests {
		defer func(req UserTranscription) {
			gcsUri, err := uploadFileToGCS(bucketName, "audio-files/"+filepath.Base(req.Filename), req.Filename)
			if err != nil {
				slog.Error("could not upload file to GCS", "file", req.Filename, "error", err)
				transcriptionChan <- make([]Word, 0)
			}
			words, err := transcribeFile(gcsUri, req.Nickname)
			if err != nil {
				slog.Error("could not transcribe file", "file", req.Filename, "error", err)
				transcriptionChan <- make([]Word, 0)
			}
			transcriptionChan <- words
		}(r)
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
	fmt.Println("\n\nTrying summary now...")
	summary, err := summarize(fullConversation)
	if err != nil {
		slog.Error("could not create summary", "error", err)
		os.Exit(1)
	}
	fmt.Printf("This is the summary:\n\n%s\n", summary)
	if err := os.WriteFile("summary.txt", []byte(fullConversation), 0600); err != nil {
		slog.Warn("could not store summary", "error", err)
	}
}

func transcribeFile(gcsUri, nickname string) ([]Word, error) {
	ctx := context.Background()
	client, err := speech.NewClient(ctx)
	if err != nil {
		return nil, errors.Join(err, errors.New("failed to create client"))
	}
	defer client.Close()

	recognitionProcess, err := client.LongRunningRecognize(ctx, &speechpb.LongRunningRecognizeRequest{
		Config: &speechpb.RecognitionConfig{
			Model:                               "latest_long",
			LanguageCode:                        "de-DE",
			EnableAutomaticPunctuation:          true,
			EnableWordTimeOffsets:               true,
			EnableSeparateRecognitionPerChannel: false,
			AudioChannelCount:                   2,
		},
		Audio: &speechpb.RecognitionAudio{
			AudioSource: &speechpb.RecognitionAudio_Uri{Uri: gcsUri},
		},
	})
	if err != nil {
		return nil, errors.Join(err, errors.New("failed to start long running recognition"))
	}

	resp, err := recognitionProcess.Wait(ctx)
	if err != nil {
		return nil, errors.Join(err, errors.New("failed to recognize"))
	}

	words := make([]Word, 0)
	for _, result := range resp.Results {
		for _, word := range result.Alternatives[0].Words {
			words = append(words, Word{Nickname: nickname, StartTime: word.StartTime.AsDuration(), Text: word.Word})
		}
	}
	return words, nil
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

func summarize(conversation string) (string, error) {
	client := openai.NewClient(openAiToken)
	ctx := context.Background()
	resp, err := client.CreateThreadAndRun(ctx, openai.CreateThreadAndRunRequest{
		Thread: openai.ThreadRequest{
			Messages: []openai.ThreadMessage{
				{
					Role:    "user",
					Content: conversation,
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
	if err := waitForRun(ctx, client, resp.ThreadID, resp.ID); err != nil {
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
