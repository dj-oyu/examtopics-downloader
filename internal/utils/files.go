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

// writeErr is a tiny accumulator that lets a sequence of fmt.Fprintf
// calls keep the first error and skip subsequent writes, so a disk-full
// or broken-pipe failure doesn't get silently lost mid-document.
type writeErr struct {
	w   io.Writer
	err error
}

func (we *writeErr) printf(format string, args ...any) {
	if we.err != nil {
		return
	}
	if _, err := fmt.Fprintf(we.w, format, args...); err != nil {
		we.err = err
	}
}

func (we *writeErr) println(s string) {
	if we.err != nil {
		return
	}
	if _, err := fmt.Fprintln(we.w, s); err != nil {
		we.err = err
	}
}

func writeFile(filename string, content any) {
	file := CreateFile(filename)
	defer file.Close()

	we := &writeErr{w: file}
	switch v := content.(type) {
	case string:
		we.println(v)
	case []string:
		for _, line := range v {
			we.println(line)
		}
	default:
		log.Printf("writeFile: unsupported content type %T", v)
		return
	}
	if we.err != nil {
		log.Printf("writeFile %s: %v", filename, we.err)
	}
}

func WriteData(dataList []models.QuestionData, outputPath string, commentBool bool, fileType string) {
	file := CreateFile(outputPath)
	defer file.Close()

	we := &writeErr{w: file}
	we.printf("# Exam Topics Questions\n\n")
	we.printf("@thatonecodes\n\n")

	for _, data := range dataList {
		if data.Title == "" {
			continue
		}

		we.printf("## %s\n\n", data.Title)
		we.printf("%s\n\n", data.Header)

		if data.Content != "" {
			we.printf("%s\n\n", data.Content)
		}

		for _, question := range data.Questions {
			we.printf("%s\n\n", question)
		}

		we.printf("**Answer: %s**\n\n", data.Answer)
		we.printf("**Timestamp: %s**\n\n", data.Timestamp)
		we.printf("[View on ExamTopics](%s)\n\n", data.QuestionLink)

		if commentBool {
			we.printf("Comments: %s\n", data.Comments)
		}

		we.printf("----------------------------------------\n\n")
	}
	if we.err != nil {
		log.Printf("WriteData %s: %v", outputPath, we.err)
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
