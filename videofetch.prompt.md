# VideoFetch Service - URL Video Download Web Service

Create a Go-based web service for downloading videos from web URLs using `yt-dlp`. The service should:

## Core Requirements
1. Run as a LAN-accessible web server
2. Accept video URLs through REST API endpoints
3. Download videos using `yt-dlp` to a specified output directory
4. Provide download status monitoring
5. Handle concurrent download requests

## Command Line Arguments
- `--output-dir` : Directory path for downloaded videos (required)
- `--port` : Server port number (default: 8080)
- `--host` : Host address to bind (default: "0.0.0.0")

## API Endpoints

### Single Video Download
```
POST /api/download_single
Content-Type: application/json

Request:
{
    "url": "https://video-site.com/watch?v=example"
}

Response:
{
    "status": "success|error",
    "message": "string",
    "id": "string"           // Unique download identifier
}
```

### Batch Video Download
```
POST /api/download
Content-Type: application/json

Request:
{
    "urls": [
        "https://video-site.com/watch?v=example1",
        "https://video-site.com/watch?v=example2"
    ]
}

Response:
{
    "status": "success|error",
    "message": "string",
    "ids": ["string"]       // Array of unique download identifiers
}
```

### Download Status
```
GET /api/status
Query Parameters:
- id (optional): Specific download ID

Response:
{
    "status": "success|error",
    "downloads": [
        {
            "id": "string",
            "url": "string",
            "progress": number,    // 0-100
            "state": "queued|downloading|completed|failed",
            "error": "string"     // If failed
        }
    ]
}
```

## Technical Requirements
1. Implement proper error handling and logging
2. Use goroutines for concurrent downloads
3. Implement download queue management
4. Validate input URLs
5. Include rate limiting for API endpoints
6. Implement graceful shutdown
7. Follow Go best practices and project structure

## Dependencies
- `yt-dlp`: Must be installed and accessible in system PATH
- Go 1.25 or later

Include appropriate documentation for API usage and error codes in the implementation.