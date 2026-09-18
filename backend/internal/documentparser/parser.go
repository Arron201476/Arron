package documentparser

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxExtractedTextBytes = 8 << 20

type Result struct {
	Text     string
	ParserID string
	Version  string
}

type Parser interface {
	Parse(context.Context, string, string) (Result, error)
	Supports(string) bool
}

type Default struct {
	pdfCommand string
}

func NewDefaultFromEnv() *Default {
	return &Default{pdfCommand: strings.TrimSpace(os.Getenv("CONTENT_AGENT_PDF_PARSER_COMMAND"))}
}

func (p *Default) Supports(extension string) bool {
	switch strings.ToLower(extension) {
	case ".docx":
		return true
	case ".pdf":
		return p != nil && p.pdfCommand != ""
	default:
		return false
	}
}

func (p *Default) Parse(ctx context.Context, path, extension string) (Result, error) {
	switch strings.ToLower(extension) {
	case ".docx":
		text, err := parseDOCX(path)
		return Result{Text: text, ParserID: "builtin.docx", Version: "1.0.0"}, err
	case ".pdf":
		if p == nil || p.pdfCommand == "" {
			return Result{}, errors.New("pdf parser is not configured")
		}
		text, err := parsePDFCommand(ctx, p.pdfCommand, path)
		return Result{Text: text, ParserID: "command.pdf", Version: "1.0.0"}, err
	default:
		return Result{}, errors.New("document type is not supported")
	}
}

func parseDOCX(path string) (string, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer archive.Close()
	var document *zip.File
	for _, file := range archive.File {
		clean := filepath.ToSlash(filepath.Clean(file.Name))
		if clean == "word/document.xml" {
			document = file
			break
		}
	}
	if document == nil || document.UncompressedSize64 > maxExtractedTextBytes*4 {
		return "", errors.New("docx document.xml is missing or too large")
	}
	reader, err := document.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	decoder := xml.NewDecoder(io.LimitReader(reader, maxExtractedTextBytes*4+1))
	var output strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch value := token.(type) {
		case xml.CharData:
			if output.Len()+len(value) > maxExtractedTextBytes {
				return "", errors.New("extracted document text is too large")
			}
			output.Write(value)
		case xml.EndElement:
			if value.Name.Local == "p" {
				output.WriteByte('\n')
			}
		}
	}
	text := strings.TrimSpace(output.String())
	if text == "" || !utf8.ValidString(text) {
		return "", errors.New("document has no valid UTF-8 text")
	}
	return text, nil
}

func parsePDFCommand(ctx context.Context, commandLine, path string) (string, error) {
	commandLine = strings.TrimSpace(commandLine)
	if commandLine == "" {
		return "", errors.New("pdf parser command is empty")
	}
	command := exec.CommandContext(ctx, commandLine, path, "-")
	var stdout, stderr bytes.Buffer
	command.Stdout = &limitedWriter{target: &stdout, remaining: maxExtractedTextBytes}
	command.Stderr = &limitedWriter{target: &stderr, remaining: 4096}
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("pdf parser failed: %w", err)
	}
	text := strings.TrimSpace(stdout.String())
	if text == "" || !utf8.ValidString(text) {
		return "", errors.New("pdf parser returned no valid UTF-8 text")
	}
	return text, nil
}

type limitedWriter struct {
	target    *bytes.Buffer
	remaining int
}

func (w *limitedWriter) Write(payload []byte) (int, error) {
	original := len(payload)
	if original > w.remaining {
		return 0, errors.New("parser output exceeds limit")
	}
	w.remaining -= original
	_, err := w.target.Write(payload)
	return original, err
}
