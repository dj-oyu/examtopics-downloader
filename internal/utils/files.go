package utils

import (
	"bufio"
	"bytes"
	"examtopics-downloader/internal/models"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/mandolyte/mdtopdf"
	"github.com/yuin/goldmark"
)

// WriteErr is an accumulator that lets a sequence of Printf / Println
// calls keep the first error and skip subsequent writes. Useful for
// long sequences of formatted writes (markdown documents, TUI loops)
// where a disk-full or broken-pipe failure should surface once at the
// end instead of being silently lost mid-document.
type WriteErr struct {
	w   io.Writer
	err error
}

// NewWriteErr wraps the given writer for sequential Printf / Println use.
func NewWriteErr(w io.Writer) *WriteErr { return &WriteErr{w: w} }

// Err returns the first write error encountered, or nil if every
// underlying write succeeded.
func (we *WriteErr) Err() error { return we.err }

// Printf formats and writes if no prior error has occurred.
func (we *WriteErr) Printf(format string, args ...any) {
	if we.Err() != nil {
		return
	}
	if _, err := fmt.Fprintf(we.w, format, args...); err != nil {
		we.err = err
	}
}

// Println writes the line if no prior error has occurred.
func (we *WriteErr) Println(s string) {
	if we.Err() != nil {
		return
	}
	if _, err := fmt.Fprintln(we.w, s); err != nil {
		we.err = err
	}
}

func writeFile(filename string, content any) {
	file := CreateFile(filename)
	defer file.Close()

	we := NewWriteErr(file)
	switch v := content.(type) {
	case string:
		we.Println(v)
	case []string:
		for _, line := range v {
			we.Println(line)
		}
	default:
		log.Printf("writeFile: unsupported content type %T", v)
		return
	}
	if we.Err() != nil {
		log.Printf("writeFile %s: %v", filename, we.Err())
	}
}

func WriteData(dataList []models.QuestionData, outputPath string, commentBool bool, fileType string) {
	file := CreateFile(outputPath)
	defer file.Close()

	we := NewWriteErr(file)
	we.Printf("# Exam Topics Questions\n\n")
	we.Printf("@thatonecodes\n\n")

	for _, data := range dataList {
		if data.Title == "" {
			continue
		}

		we.Printf("## %s\n\n", data.Title)
		we.Printf("%s\n\n", data.Header)

		if data.Content != "" {
			we.Printf("%s\n\n", data.Content)
		}

		for _, question := range data.Questions {
			we.Printf("%s\n\n", question)
		}

		we.Printf("**Answer: %s**\n\n", data.Answer)
		we.Printf("**Timestamp: %s**\n\n", data.Timestamp)
		we.Printf("[View on ExamTopics](%s)\n\n", data.QuestionLink)

		if commentBool {
			we.Printf("Comments: %s\n", data.Comments)
		}

		we.Printf("----------------------------------------\n\n")
	}
	if we.Err() != nil {
		log.Printf("WriteData %s: %v", outputPath, we.Err())
		return
	}

	switch fileType {
	case "pdf":
		mdContent, err := os.ReadFile(outputPath)
		if err != nil {
			log.Printf("failed to read markdown file: %v", err)
			return
		}

		opts := []mdtopdf.RenderOption{
			mdtopdf.IsHorizontalRuleNewPage(true), // treat --- as new page
		}

		pdfName := strings.TrimSuffix(outputPath, ".md") + ".pdf"
		renderer := mdtopdf.NewPdfRenderer("portrait", "A4", pdfName, "", opts, mdtopdf.LIGHT)
		if err := renderer.Process(mdContent); err != nil {
			log.Printf("mdtopdf conversion failed: %v", err)
			return
		}
		deleteMarkdownFile(outputPath)
	case "html":
		mdBytes, err := os.ReadFile(outputPath)
		if err != nil {
			log.Printf("failed to read file for html conversion: %v", err)
			return
		}

		html, err := mdToHTML(mdBytes)
		if err != nil {
			log.Printf("mdtohtml conversion failed: %v", err)
			return
		}

		fileName := strings.TrimSuffix(outputPath, ".md") + ".html"
		err = os.WriteFile(fileName, html, 0644)
		if err != nil {
			log.Printf("failed to write html file: %v", err)
			return
		}
		deleteMarkdownFile(outputPath)
	case "text":
		mdBytes, err := os.ReadFile(outputPath)
		if err != nil {
			log.Printf("failed to read file for text conversion: %v", err)
			return
		}

		txt := mdToText(string(mdBytes))

		fileName := strings.TrimSuffix(outputPath, ".md") + ".txt"
		err = os.WriteFile(fileName, []byte(txt), 0644)
		if err != nil {
			log.Printf("failed to write text file: %v", err)
			return
		}
		deleteMarkdownFile(outputPath)
	}
}

func mdToHTML(md []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := goldmark.Convert(md, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func deleteMarkdownFile(filePath string) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Delete Markdown file after conversion? (y/n): ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))

	if input == "y" || input == "yes" {
		fmt.Println("Deleting file...")
		if err := os.Remove(filePath); err != nil {
			log.Printf("delete %s: %v", filePath, err)
		}
	} else {
		fmt.Println("Keeping file.")
	}
}

func mdToText(md string) string {
	text := md
	// Markdown headers (#, ##, ###)
	header := regexp.MustCompile(`(?m)^#{1,6}\s*`)
	text = header.ReplaceAllString(text, "")
	// bold/italic symbols (*, **, _)
	formatting := regexp.MustCompile(`(\*\*|\*|__|_)`)
	text = formatting.ReplaceAllString(text, "")
	// links but keep link text [text](url) → text
	link := regexp.MustCompile(`\[(.*?)\]\(.*?\)`)
	text = link.ReplaceAllString(text, "$1")
	// images ![alt](url)
	image := regexp.MustCompile(`!\[.*?\]\(.*?\)`)
	text = image.ReplaceAllString(text, "")

	return text
}

func SaveLinks(filename string, links []models.QuestionData) {
	var fullLinks []string
	for _, link := range links {
		fullLinks = append(fullLinks, link.QuestionLink)
	}
	writeFile(filename, fullLinks)
}
