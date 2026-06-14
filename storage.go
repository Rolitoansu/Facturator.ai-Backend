package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// SupabaseStorage handles file uploads to Supabase Storage.
type SupabaseStorage struct {
	ProjectURL string
	ServiceKey string // service_role key for server-side uploads
	BucketName string
	client     *http.Client
}

// StorageUploadResult contains the result of a file upload.
type StorageUploadResult struct {
	Path      string `json:"path"`       // e.g. "user-uuid/receipt-uuid.jpg"
	PublicURL string `json:"publicUrl"`  // full URL to access the file
}

// NewSupabaseStorage creates a new storage client.
func NewSupabaseStorage(projectURL, serviceKey, bucketName string) *SupabaseStorage {
	return &SupabaseStorage{
		ProjectURL: strings.TrimSuffix(projectURL, "/"),
		ServiceKey: serviceKey,
		BucketName: bucketName,
		client:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Upload uploads a file to Supabase Storage.
// The file is stored at: {bucketName}/{userID}/{fileName}
func (s *SupabaseStorage) Upload(userID string, fileName string, contentType string, fileData io.Reader) (*StorageUploadResult, error) {
	// Build the storage path: userID/fileName
	objectPath := fmt.Sprintf("%s/%s", userID, fileName)

	// Read all data into a buffer (Supabase needs Content-Length)
	buf := &bytes.Buffer{}
	written, err := io.Copy(buf, fileData)
	if err != nil {
		return nil, fmt.Errorf("error reading file data: %w", err)
	}

	// POST to Supabase Storage API
	url := fmt.Sprintf("%s/storage/v1/object/%s/%s", s.ProjectURL, s.BucketName, objectPath)

	req, err := http.NewRequest("POST", url, buf)
	if err != nil {
		return nil, fmt.Errorf("error creating upload request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.ServiceKey)
	req.Header.Set("apikey", s.ServiceKey)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Length", fmt.Sprintf("%d", written))
	// Upsert: overwrite if exists
	req.Header.Set("x-upsert", "true")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error uploading to Supabase Storage: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Supabase Storage returned status %d: %s", resp.StatusCode, string(body))
	}

	// Build the signed/public URL
	publicURL := fmt.Sprintf("%s/storage/v1/object/public/%s/%s", s.ProjectURL, s.BucketName, objectPath)

	return &StorageUploadResult{
		Path:      objectPath,
		PublicURL: publicURL,
	}, nil
}

// GetSignedURL creates a time-limited signed URL for a private file.
func (s *SupabaseStorage) GetSignedURL(objectPath string, expiresIn int) (string, error) {
	url := fmt.Sprintf("%s/storage/v1/object/sign/%s/%s", s.ProjectURL, s.BucketName, objectPath)

	body := fmt.Sprintf(`{"expiresIn":%d}`, expiresIn)
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("error creating sign request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.ServiceKey)
	req.Header.Set("apikey", s.ServiceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error getting signed URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Supabase sign URL returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse JSON response to get signedURL
	respBody, _ := io.ReadAll(resp.Body)
	// Response is: {"signedURL": "/storage/v1/object/sign/..."}
	// We need to prepend the project URL
	signedPath := strings.TrimPrefix(string(respBody), `{"signedURL":"`)
	signedPath = strings.TrimSuffix(signedPath, `"}`)
	
	return s.ProjectURL + signedPath, nil
}

// Delete removes a file from Supabase Storage.
func (s *SupabaseStorage) Delete(objectPath string) error {
	url := fmt.Sprintf("%s/storage/v1/object/%s/%s", s.ProjectURL, s.BucketName, objectPath)

	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("error creating delete request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.ServiceKey)
	req.Header.Set("apikey", s.ServiceKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("error deleting from Supabase Storage: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Supabase Storage delete returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// DetectContentType returns the MIME type based on file extension.
func DetectContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	case ".gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}
