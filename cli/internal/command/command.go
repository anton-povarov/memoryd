// Package command implements the small amount of CLI behavior above the
// generated memoryd HTTP client.
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"

	"github.com/anton-povarov/memoryd/cli/internal/api"
	"github.com/google/uuid"
)

const DefaultServerURL = "http://127.0.0.1:8080"

const (
	maxPageSize       = 100
	asciiControlLimit = 0x20
	asciiDelete       = 0x7f
)

func ServerURL(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if configured := os.Getenv("MEMORYD_URL"); configured != "" {
		return configured
	}
	return DefaultServerURL
}

func Put(ctx context.Context, serverURL, path string, stdout, stderr io.Writer) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect import candidate: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("import candidate %q is not a regular file", path)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve import path: %w", err)
	}
	fmt.Fprintf(stderr, "uploading %s (%d bytes)\n", filepath.Base(absolutePath), info.Size())

	bodyReader, bodyWriter := io.Pipe()
	multipartWriter := multipart.NewWriter(bodyWriter)
	uploadErrors := make(chan error, 1)
	go func() {
		uploadErrors <- writeMultipart(bodyWriter, multipartWriter, absolutePath, info)
	}()

	client, err := api.NewClient(apiBaseURL(serverURL))
	if err != nil {
		_ = bodyReader.Close()
		return fmt.Errorf("create memoryd client: %w", err)
	}
	response, requestErr := client.ImportMemoryWithBody(
		ctx, multipartWriter.FormDataContentType(), bodyReader,
	)
	uploadErr := <-uploadErrors
	if requestErr != nil {
		return fmt.Errorf("import Memory: %w", requestErr)
	}
	if uploadErr != nil && !errors.Is(uploadErr, io.ErrClosedPipe) {
		response.Body.Close()
		return uploadErr
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusConflict {
		parsed, err := api.ParseImportMemoryResponse(response)
		if err != nil {
			return fmt.Errorf("decode Duplicate response: %w", err)
		}
		if parsed.JSON409 == nil || parsed.JSON409.ExistingMemory == nil {
			return fmt.Errorf("duplicate response did not identify the existing Memory")
		}
		fmt.Fprintf(
			stderr,
			"duplicate: using existing Memory %s\n",
			parsed.JSON409.ExistingMemory.Id,
		)
		_, err = fmt.Fprintln(stdout, parsed.JSON409.ExistingMemory.Id)
		return err
	}
	if response.StatusCode != http.StatusCreated {
		parsed, err := api.ParseImportMemoryResponse(response)
		if err != nil {
			return fmt.Errorf("import failed with HTTP %s", response.Status)
		}
		return responseError(parsed)
	}
	parsed, err := api.ParseImportMemoryResponse(response)
	if err != nil {
		return fmt.Errorf("decode committed Memory: %w", err)
	}
	if parsed.JSON201 == nil {
		return errors.New("successful import response did not contain a Memory")
	}
	_, err = fmt.Fprintln(stdout, parsed.JSON201.Id)
	return err
}

func writeMultipart(
	pipe *io.PipeWriter,
	writer *multipart.Writer,
	absolutePath string,
	info os.FileInfo,
) (err error) {
	defer func() {
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		_ = pipe.CloseWithError(err)
	}()
	filename := filepath.Base(absolutePath)
	contextJSON, err := json.Marshal(struct {
		FullPath             string `json:"full_path"`
		FilesystemModifiedAt string `json:"filesystem_modified_at"`
	}{
		FullPath:             absolutePath,
		FilesystemModifiedAt: info.ModTime().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	})
	if err != nil {
		return fmt.Errorf("encode Import Context: %w", err)
	}
	contextHeader := make(textproto.MIMEHeader)
	contextHeader.Set("Content-Disposition", `form-data; name="import_context"`)
	contextHeader.Set("Content-Type", "application/json")
	contextPart, err := writer.CreatePart(contextHeader)
	if err != nil {
		return fmt.Errorf("create Import Context part: %w", err)
	}
	if _, err := contextPart.Write(contextJSON); err != nil {
		return fmt.Errorf("write Import Context: %w", err)
	}
	contentType := mime.TypeByExtension(filepath.Ext(filename))
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".md", ".markdown":
		contentType = "text/markdown"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	disposition := mime.FormatMediaType("form-data", map[string]string{
		"name":     "file",
		"filename": filename,
	})
	if disposition == "" {
		return fmt.Errorf("format multipart filename %q", filename)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", disposition)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("create multipart Blob: %w", err)
	}
	content, err := os.Open(absolutePath)
	if err != nil {
		return fmt.Errorf("open import candidate: %w", err)
	}
	defer content.Close()
	if _, err := io.Copy(part, content); err != nil {
		return fmt.Errorf("stream import candidate: %w", err)
	}
	return nil
}

func responseError(response *api.ImportMemoryResponse) error {
	var problem *api.Error
	switch {
	case response.JSON400 != nil:
		problem = response.JSON400
	case response.JSON413 != nil:
		problem = response.JSON413
	case response.JSON500 != nil:
		problem = response.JSON500
	}
	if problem != nil {
		return importHTTPError(response, problem.Code, problem.Message)
	}
	return importHTTPError(response, "", "")
}

func importHTTPError(response *api.ImportMemoryResponse, code, message string) error {
	status := "import failed with HTTP " + response.Status()
	if code != "" && message != "" {
		return fmt.Errorf("%s: %s: %s", status, code, message)
	}
	if code != "" {
		return fmt.Errorf("%s: %s", status, code)
	}
	if message != "" && message != http.StatusText(response.StatusCode()) {
		return fmt.Errorf("%s: %s", status, message)
	}
	return errors.New(status)
}

func Get(
	ctx context.Context,
	serverURL string,
	memoryID uuid.UUID,
	output string,
	force bool,
	stdout, stderr io.Writer,
) error {
	fmt.Fprintf(stderr, "downloading Memory %s\n", memoryID)
	client, err := api.NewClient(apiBaseURL(serverURL))
	if err != nil {
		return fmt.Errorf("create memoryd client: %w", err)
	}
	response, err := client.GetMemoryContent(ctx, memoryID)
	if err != nil {
		return fmt.Errorf("download Memory: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		parsed, parseErr := api.ParseGetMemoryContentResponse(response)
		if parseErr == nil && parsed.JSON404 != nil {
			return fmt.Errorf("%s: %s", parsed.JSON404.Code, parsed.JSON404.Message)
		}
		if parseErr != nil {
			return fmt.Errorf("download failed with HTTP %s", response.Status)
		}
		return fmt.Errorf("download failed with HTTP %s", parsed.Status())
	}
	defer response.Body.Close()

	if output == "-" {
		written, err := io.Copy(stdout, response.Body)
		if err == nil {
			fmt.Fprintf(stderr, "downloaded %d bytes\n", written)
		}
		return err
	}

	if output == "" {
		output = filenameFromDisposition(
			response.Header.Get("Content-Disposition"),
			memoryID.String(),
		)
	}
	fmt.Fprintf(stderr, "saving Blob to %s\n", output)
	if !force {
		if _, err := os.Lstat(output); err == nil {
			return fmt.Errorf("destination %q already exists (use --force to replace it)", output)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect destination: %w", err)
		}
	}

	directory := filepath.Dir(output)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(output)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary download: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "could not remove temporary download: %v\n", err)
		}
	}()
	written, err := io.Copy(temporary, response.Body)
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("download Blob: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary download: %w", err)
	}
	if err := os.Rename(temporaryPath, output); err != nil {
		return fmt.Errorf("publish download: %w", err)
	}
	fmt.Fprintf(stderr, "downloaded %d bytes\n", written)

	fmt.Fprintf(stdout, "%s\n", output) // indicate the filename we've written to
	return nil
}

func Info(ctx context.Context, serverURL string, memoryID uuid.UUID, stdout io.Writer) error {
	client, err := api.NewClient(apiBaseURL(serverURL))
	if err != nil {
		return fmt.Errorf("create memoryd client: %w", err)
	}
	response, err := client.GetMemory(ctx, memoryID)
	if err != nil {
		return fmt.Errorf("get Memory details: %w", err)
	}
	defer response.Body.Close()
	parsed, err := api.ParseGetMemoryResponse(response)
	if err != nil {
		return fmt.Errorf("decode Memory details: %w", err)
	}
	if parsed.JSON404 != nil {
		return fmt.Errorf("%s: %s", parsed.JSON404.Code, parsed.JSON404.Message)
	}
	if parsed.JSON200 == nil {
		return fmt.Errorf("get Memory details failed with HTTP %s", parsed.Status())
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, parsed.Body, "", "  "); err != nil {
		return fmt.Errorf("format Memory details: %w", err)
	}
	formatted.WriteByte('\n')
	_, err = formatted.WriteTo(stdout)
	return err
}

func List(
	ctx context.Context,
	serverURL string,
	count int,
	all, short bool,
	stdout io.Writer,
) error {
	client, err := api.NewClient(apiBaseURL(serverURL))
	if err != nil {
		return fmt.Errorf("create memoryd client: %w", err)
	}
	items := make([]api.MemorySummary, 0)
	var cursor *api.Cursor
	for {
		pageSize := maxPageSize
		if !all && count-len(items) < pageSize {
			pageSize = count - len(items)
		}
		limit := api.Limit(pageSize)
		response, err := client.BrowseMemories(ctx, &api.BrowseMemoriesParams{
			Limit:  &limit,
			Cursor: cursor,
		})
		if err != nil {
			return fmt.Errorf("list Memories: %w", err)
		}
		parsed, err := api.ParseBrowseMemoriesResponse(response)
		response.Body.Close()
		if err != nil {
			return fmt.Errorf("decode Memory page: %w", err)
		}
		if parsed.JSON400 != nil {
			return fmt.Errorf("%s: %s", parsed.JSON400.Code, parsed.JSON400.Message)
		}
		if parsed.JSON200 == nil {
			return fmt.Errorf("list Memories failed with HTTP %s", parsed.Status())
		}
		page := parsed.JSON200
		if !all && len(page.Items) > count-len(items) {
			page.Items = page.Items[:count-len(items)]
		}
		items = append(items, page.Items...)
		if (!all && len(items) >= count) || page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if short {
		for _, item := range items {
			if _, err := fmt.Fprintln(stdout, item.Id); err != nil {
				return err
			}
		}
		return nil
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(items)
}

func filenameFromDisposition(disposition, fallback string) string {
	_, parameters, err := mime.ParseMediaType(disposition)
	if err == nil {
		if name := safeFilename(parameters["filename"]); name != "" {
			return name
		}
	}
	return safeFilename(fallback)
}

func safeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < asciiControlLimit || r == asciiDelete {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == "/" {
		return "memory"
	}
	return name
}

func apiBaseURL(serverURL string) string {
	serverURL = strings.TrimRight(serverURL, "/")
	if strings.HasSuffix(serverURL, "/api/v0") {
		return serverURL
	}
	return serverURL + "/api/v0"
}
