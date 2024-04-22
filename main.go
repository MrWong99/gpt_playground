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
	"time"

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

//go:embed openai.token
var openAiToken string

//go:embed assistant.id
var openAiAssistantId string

func main() {
	onlySummary()
	//fullTranscribe()
}

func onlySummary() {
	fmt.Println("Trying summary now...")
	summary, err := summarizeFromFile("transcription.txt")
	if err != nil {
		slog.Error("could not create summary", "error", err)
		os.Exit(1)
	}
	fmt.Printf("This is the summary:\n\n%s\n", summary)
	if err := os.WriteFile("summary.txt", []byte(summary), 0600); err != nil {
		slog.Warn("could not store summary", "error", err)
	}
}

func fullTranscribe() {
	requests := []UserTranscription{
		{
			Nickname: "GameMaster",
			Filename: "input/1-mrwong99_0.flac",
		},
		{
			Nickname: "Schachar",
			Filename: "input/2-flonk3141_0.flac",
		},
		{
			Nickname: "Tharkan",
			Filename: "input/3-lazerlenny369_0.flac",
		},
		{
			Nickname: "Amon",
			Filename: "input/4-streuz_0.flac",
		},
		{
			Nickname: "Berta",
			Filename: "input/5-fee9880_0.flac",
		},
	}
	allWords := make([]Word, 0)
	// transcriptionChan := make(chan []Word, len(requests))
	for _, r := range requests {
		if words, err := transcribeWhisperx(r.Filename, r.Nickname); err != nil {
			slog.Error("could not transcribe", "request", r, "error", err)
			os.Exit(1)
		} else {
			allWords = append(allWords, words...)
		}
		//go func(req UserTranscription, c chan<- []Word) {
		/*
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
			words, err := transcribeFileOpenAI(req.Filename, req.Nickname)
			if err != nil {
				slog.Error("could not transcribe file", "file", req.Filename, "error", err)
				c <- make([]Word, 0)
				return
			}
			c <- words
		*/
		//}(r, transcriptionChan)
	}
	/*for i := 0; i < len(requests); i++ {
		allWords = append(allWords, <-transcriptionChan...)
	}*/
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
	words := make([]Word, 0)
	for _, segment := range res.Segments {
		for _, word := range segment.Words {
			words = append(words, Word{
				Nickname:  nickname,
				Text:      word.Word,
				StartTime: time.Duration(word.Start),
			})
		}
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
*/

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
	client := openai.NewClient(openAiToken)
	ctx := context.Background()
	content, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	fileResp, err := client.CreateFileBytes(ctx, openai.FileBytesRequest{
		Name:    file,
		Bytes:   content,
		Purpose: openai.PurposeAssistants,
	})
	if err != nil {
		return "", err
	}
	resp, err := client.CreateThreadAndRun(ctx, openai.CreateThreadAndRunRequest{
		Thread: openai.ThreadRequest{
			Messages: []openai.ThreadMessage{
				{
					Role:    "user",
					Content: "Bitte fasse die Datei " + fileResp.ID + " zusammen.",
					FileIDs: []string{fileResp.ID},
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
	convo := ""
	for _, message := range messages.Messages {
		convo += message.Role + ":\n"
		for _, c := range message.Content {
			convo += c.Text.Value + "\n"
		}
		convo += "\n\n"
	}
	if delResp, err := client.DeleteThread(ctx, resp.ThreadID); err != nil || !delResp.Deleted {
		slog.Warn("could not delete thread after finish", "threadID", resp.ThreadID, "response", delResp, "error", err)
	}
	return convo, nil
}

func summarize(lines []Line) (string, error) {
	client := openai.NewClient(openAiToken)
	ctx := context.Background()
	linesPerRequest := make([]string, 0)
	currentText := ""
	for _, line := range lines {
		lineText := line.String()
		if len(currentText)+len("\n"+lineText)+len(NextPhrase) > 32768 { // Threads have max allowed length of 32768
			currentText += NextPhrase
			linesPerRequest = append(linesPerRequest, currentText)
			currentText = lineText
		} else {
			currentText += "\n" + lineText
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
		_, err := client.CreateMessage(ctx, resp.ThreadID, openai.MessageRequest{
			Role:    "user",
			Content: text,
		})
		if err != nil {
			return "", err
		}
		runs, err := client.ListRuns(ctx, resp.ThreadID, openai.Pagination{})
		if err != nil {
			return "", err
		}
		for _, run := range runs.Runs {
			if run.Status == openai.RunStatusInProgress || run.Status == openai.RunStatusQueued {
				// still running
				runID = runs.Runs[0].ID
				break
			}
		}
	}
	if err := waitForRun(ctx, client, resp.ThreadID, runID); err != nil {
		return "", err
	}
	time.Sleep(1 * time.Second)
	runs, err := client.ListRuns(ctx, resp.ThreadID, openai.Pagination{})
	if err != nil {
		return "", err
	}
	anyRunning := false
	for _, run := range runs.Runs {
		if run.Status == openai.RunStatusInProgress || run.Status == openai.RunStatusQueued {
			// still running
			runID = runs.Runs[0].ID
			anyRunning = true
			break
		}
	}
	if anyRunning {
		if err := waitForRun(ctx, client, resp.ThreadID, runID); err != nil {
			return "", err
		}
	}
	messages, err := client.ListMessage(ctx, resp.ThreadID, nil, nil, nil, nil)
	if err != nil {
		return "", err
	}
	content := ""
	for _, message := range messages.Messages {
		content += message.Role + ":\n"
		for _, c := range message.Content {
			content += c.Text.Value + "\n"
		}
		content += "\n\n"
	}
	if delResp, err := client.DeleteThread(ctx, resp.ThreadID); err != nil || !delResp.Deleted {
		slog.Warn("could not delete thread after finish", "threadID", resp.ThreadID, "response", delResp, "error", err)
	}
	return content, nil
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
