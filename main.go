package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sashabaranov/go-openai"
)

type AudioFile struct {
	Nickname string
	Filename string
}

type Line struct {
	Nickname string
	Words    []Word
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
	StartTime float64
}

func (w *Word) String() string {
	return w.Text
}

//go:embed openai.token
var openAiToken string

//go:embed summary_system_prompt.txt
var summarySystemPrompt string

func main() {
	files, err := os.ReadDir("input")
	if err != nil {
		slog.Error("could not open input/ directory", "error", err)
		os.Exit(1)
	}
	requests := make([]AudioFile, 0)
	for _, file := range files {
		ext := filepath.Ext(file.Name())
		if ext != ".flac" {
			continue
		}
		requests = append(requests, AudioFile{
			Nickname: strings.TrimSuffix(filepath.Base(file.Name()), ext),
			Filename: filepath.Join("input/", file.Name()),
		})
	}

	allWords := make([]Word, 0)
	for _, r := range requests {
		if words, err := transcribeWhisperx(r.Filename, r.Nickname); err != nil {
			slog.Error("could not transcribe", "request", r, "error", err)
			os.Exit(1)
		} else {
			allWords = append(allWords, words...)
		}
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
	summary, err := summarize(fullConversation)
	if err != nil {
		slog.Error("could not create summary", "error", err)
		os.Exit(1)
	}
	fmt.Printf("This is the summary:\n\n%s\n", summary)
	if err := os.WriteFile("summary.txt", []byte(summary), 0600); err != nil {
		slog.Warn("could not store summary", "error", err)
	}
}

func transcribeWhisperx(file, nickname string) ([]Word, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("whisperx",
		"--model", "large-v3", "--align_model", "WAV2VEC2_ASR_LARGE_LV60K_960H",
		"--batch_size", "4", "--task", "transcribe", "--output_dir", "output", "--output_format", "json", "--language", "de", abs)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, errors.Join(err, fmt.Errorf("could not run %s, output:\n%s", cmd, out))
	}
	outFile := filepath.Join("output/", strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))+".json")
	f, err := os.Open(outFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var res WhisperxResult
	if err := json.NewDecoder(f).Decode(&res); err != nil {
		return nil, err
	}
	words := make([]Word, len(res.WordSegments))
	lastStart := float64(0)
	for i, word := range res.WordSegments {
		w := Word{
			Nickname: nickname,
			Text:     word.Word,
		}
		if word.Start != 0 {
			w.StartTime = word.Start
			lastStart = word.Start
		} else {
			w.StartTime = lastStart
		}
		words[i] = w
	}
	return words, nil
}

type WhisperxResult struct {
	Segments []struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Text  string  `json:"text"`
		Words []struct {
			Word  string  `json:"word"`
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Score float64 `json:"score"`
		} `json:"words"`
	} `json:"segments"`
	WordSegments []struct {
		Word  string  `json:"word"`
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Score float64 `json:"score"`
	} `json:"word_segments"`
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
		if lastWord.Nickname == currentWord.Nickname && (currentWord.StartTime-lastWord.StartTime) < 7 {
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

func constructLinesFromFile(file string) ([]Line, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	textLines := strings.Split(string(content), "\n")
	lines := make([]Line, len(textLines))
	for iL, l := range textLines {
		split := strings.SplitN(l, ":", 2)
		wordSplit := strings.Split(split[1], " ")
		line := Line{
			Nickname: split[0],
			Words:    make([]Word, len(wordSplit)),
		}
		for iW, word := range wordSplit {
			line.Words[iW] = Word{
				Nickname: line.Nickname,
				Text:     word,
			}
		}
		lines[iL] = line
	}
	return lines, nil
}

func asLine(words []Word) Line {
	return Line{
		Nickname: words[0].Nickname,
		Words:    words,
	}
}

const NextPhrase = "\nNEXT CHUNK AFTER RESPONSE"

func summarizeFromFile(file string) (string, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return summarize(string(content))
}

func summarize(transcription string) (string, error) {
	client := openai.NewClient(openAiToken)
	ctx := context.Background()
	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: "gpt-4-turbo-preview",
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    "system",
				Content: summarySystemPrompt,
			},
			{
				Role:    "user",
				Content: transcription,
			},
		},
	})
	if err != nil {
		return "", err
	}
	allResponses := ""
	for i, choice := range resp.Choices {
		if i == 0 {
			allResponses = choice.Message.Content
		} else {
			allResponses = fmt.Sprintf("%s\n\n%s", allResponses, choice.Message.Content)
		}
	}
	if allResponses == "" {
		return "", errors.New("no summary returned by ChatGPT")
	}
	return allResponses, nil
}
