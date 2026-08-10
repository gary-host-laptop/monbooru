package metadata

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"strings"

	"github.com/monbooru/monbooru/internal/models"
)

// ComicInfoMaxRawXML caps the raw XML body persisted on
// manga_metadata.raw_xml. Anything larger is truncated and the row
// surfaces a "(truncated)" affordance in the metadata panel.
const ComicInfoMaxRawXML = 64 * 1024

// comicInfoXML is the on-disk schema, name-for-name with the
// ComicRack / Anansi standard. Pages are intentionally absent: the
// authoritative page count lives on images.page_count, derived from
// the archive's image entries.
type comicInfoXML struct {
	XMLName         xml.Name `xml:"ComicInfo"`
	Title           string   `xml:"Title,omitempty"`
	Series          string   `xml:"Series,omitempty"`
	Number          string   `xml:"Number,omitempty"`
	Volume          string   `xml:"Volume,omitempty"`
	Count           *int     `xml:"Count,omitempty"`
	Summary         string   `xml:"Summary,omitempty"`
	Notes           string   `xml:"Notes,omitempty"`
	Year            *int     `xml:"Year,omitempty"`
	Month           *int     `xml:"Month,omitempty"`
	Day             *int     `xml:"Day,omitempty"`
	Writer          string   `xml:"Writer,omitempty"`
	Penciller       string   `xml:"Penciller,omitempty"`
	Inker           string   `xml:"Inker,omitempty"`
	Colorist        string   `xml:"Colorist,omitempty"`
	Letterer        string   `xml:"Letterer,omitempty"`
	CoverArtist     string   `xml:"CoverArtist,omitempty"`
	Editor          string   `xml:"Editor,omitempty"`
	Publisher       string   `xml:"Publisher,omitempty"`
	Imprint         string   `xml:"Imprint,omitempty"`
	Genre           string   `xml:"Genre,omitempty"`
	Web             string   `xml:"Web,omitempty"`
	LanguageISO     string   `xml:"LanguageISO,omitempty"`
	Format          string   `xml:"Format,omitempty"`
	Manga           string   `xml:"Manga,omitempty"`
	AgeRating       string   `xml:"AgeRating,omitempty"`
	CommunityRating *float64 `xml:"CommunityRating,omitempty"`
	PageCount       *int     `xml:"PageCount,omitempty"`
}

// MarshalComicInfo renders the ComicInfo.xml document for a generated
// archive: the collection label as Title and the page count. The inverse
// of ParseComicInfo's read path, sharing its schema. No XML declaration
// is emitted, matching how most archivers write the file.
func MarshalComicInfo(title string, pageCount int) ([]byte, error) {
	doc := comicInfoXML{Title: title, PageCount: &pageCount}
	return xml.MarshalIndent(doc, "", "  ")
}

// ParseComicInfo locates ComicInfo.xml at the archive root
// (case-insensitive root match), parses it, and returns a populated
// MangaMetadata. Returns (nil, nil) when no ComicInfo file exists or
// when the file is at a non-root path. Returns (nil, err) only on a
// genuine read error; XML parse failures are logged at debug by the
// caller and surface as nil so the manga itself still ingests.
func ParseComicInfo(zr *zip.Reader) (*models.MangaMetadata, error) {
	if zr == nil {
		return nil, nil
	}
	var entry *zip.File
	for _, f := range zr.File {
		// ComicInfo lives at the archive root by spec; reject any
		// nested path so a stray comicinfo.xml inside a chapter folder
		// isn't picked up.
		if strings.ContainsRune(f.Name, '/') {
			continue
		}
		if strings.EqualFold(f.Name, "ComicInfo.xml") {
			entry = f
			break
		}
	}
	if entry == nil {
		return nil, nil
	}
	rc, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, int64(ComicInfoMaxRawXML)+1))
	if err != nil {
		return nil, err
	}
	truncated := len(body) > ComicInfoMaxRawXML
	if truncated {
		body = body[:ComicInfoMaxRawXML]
	}
	var doc comicInfoXML
	// Malformed XML still earns a row so the truncated raw_xml is
	// preserved for the operator to inspect; populated fields stay
	// zero-valued.
	_ = xml.Unmarshal(body, &doc)
	return &models.MangaMetadata{
		Title:           strings.TrimSpace(doc.Title),
		Series:          strings.TrimSpace(doc.Series),
		Number:          strings.TrimSpace(doc.Number),
		Volume:          strings.TrimSpace(doc.Volume),
		Count:           doc.Count,
		Summary:         strings.TrimSpace(doc.Summary),
		Notes:           strings.TrimSpace(doc.Notes),
		Year:            doc.Year,
		Month:           doc.Month,
		Day:             doc.Day,
		Writer:          strings.TrimSpace(doc.Writer),
		Penciller:       strings.TrimSpace(doc.Penciller),
		Inker:           strings.TrimSpace(doc.Inker),
		Colorist:        strings.TrimSpace(doc.Colorist),
		Letterer:        strings.TrimSpace(doc.Letterer),
		CoverArtist:     strings.TrimSpace(doc.CoverArtist),
		Editor:          strings.TrimSpace(doc.Editor),
		Publisher:       strings.TrimSpace(doc.Publisher),
		Imprint:         strings.TrimSpace(doc.Imprint),
		Genre:           strings.TrimSpace(doc.Genre),
		Web:             strings.TrimSpace(doc.Web),
		LanguageISO:     strings.TrimSpace(doc.LanguageISO),
		Format:          strings.TrimSpace(doc.Format),
		Manga:           strings.TrimSpace(doc.Manga),
		AgeRating:       strings.TrimSpace(doc.AgeRating),
		CommunityRating: doc.CommunityRating,
		XMLPageCount:    doc.PageCount,
		RawXML:          string(body),
	}, nil
}
