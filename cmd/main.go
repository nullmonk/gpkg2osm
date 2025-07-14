package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/lc-dmx/osm-go/osmpbf"
	"github.com/lc-dmx/osm-go/osmpbf/entity"
	_ "github.com/mattn/go-sqlite3" // SQLite driver
	"github.com/paulmach/osm"
	"github.com/spf13/pflag"
	"github.com/twpayne/go-geom"
	"github.com/twpayne/go-geom/encoding/wkb"
)

const (
	programVersion = "v0.1.0"
	usageHeader    = `gpkg2osm %s
Usage: %s [flags] <input.gpkg> [output.osm.pbf|output.osm.xml|-]

Converts a GeoPackage file to an OpenStreetMap PBF or XML file.
Output format is determined by the output file extension (.osm.pbf for PBF, .osm.xml for XML).

Arguments:
  <input.gpkg>       Path to the input GeoPackage file.
  [output.osm.pbf|output.osm.xml|-]   Optional path for the output OSM file.
                     If omitted, the program will print a summary of conversions.
                     Use '-' for stdout (implies XML output).

Flags:
`
	usageExamples = `
Examples:
  %s file.gpkg                           # Print conversion summary (columns/fields) without converting.
  %s file.gpkg file.osm.pbf              # Convert file.gpkg to file.osm.pbf.
  %s file.gpkg file.osm.xml              # Convert file.gpkg to file.osm.xml.
  %s file.gpkg -                         # Convert file.gpkg to OSM XML and print to stdout.
`
	summaryHeaderTemplate = `Analyzing GeoPackage: %s
---------------------------------------
Detected Layers and Suggested OSM Tag Mappings:
`
	summaryLayerTemplate = `
Layer: %s (Geometry: %s)
  Columns: %v
`
)

// Gpkg geometry types that we allow
var valid_geoms = map[string]geom.T{
	"MULTIPOLYGON":    &geom.MultiPolygon{},
	"POLYGON":         &geom.Polygon{},
	"MULTILINESTRING": &geom.MultiLineString{},
	"LINESTRING":      &geom.LineString{},
	"POINT":           &geom.Point{},
}

// ExportLayer holds information about which columns get exported to the OSM file
type ExportLayer struct {
	Name          string   // Also Table Name
	Tags          []string // Columns that directly map to an OSM tag
	OSMJsonField  bool     // True if this layer has the "osm_tags" JSON column
	GeometryField string   // Name of geometery colum
	GeometryType  string
	SRS           int32
	Z             sql.NullBool
	M             sql.NullBool
}

// Get the Query that is used to read elements from this layer
func (l *ExportLayer) Query() string {
	// If NO other tag fields exist, its easy, simply return geom and osm_tags
	if l.OSMJsonField && len(l.Tags) == 0 {
		return fmt.Sprintf("SELECT %s, osm_tags FROM %s", l.GeometryField, l.Name)
	}
	// More complicated, we have tags, so we need to get them as JSON
	cols := make([]string, 0, len(l.Tags)*2)
	for _, t := range l.Tags {
		cols = append(cols, "'"+t+"'", t)
	}
	json_tags := fmt.Sprintf("json_object(%s)", strings.Join(cols, ", "))
	// Merge the tags with the osm_tags field
	if l.OSMJsonField {
		json_tags = fmt.Sprintf("json_patch(%s, osm_tags)", json_tags)
	}

	// Remove NULLs
	qry := `SELECT %s, COALESCE((SELECT json_group_object(key, value)
	FROM json_each(%s)
	WHERE value IS NOT NULL), '{}') AS osm_tags FROM %s`
	return fmt.Sprintf(qry, l.GeometryField, json_tags, l.Name)
}

// Validate if this is an exportable layer or not
func (l *ExportLayer) Validate() error {
	if !l.OSMJsonField && len(l.Tags) == 0 {
		return fmt.Errorf("no OSM tags")
	}
	if l.SRS != 4326 {
		return fmt.Errorf("invalid SRS, must be EPSG:4326")
	}
	if _, ok := valid_geoms[l.GeometryType]; !ok {
		return fmt.Errorf("invalid geometry type")
	}
	return nil
}

// This is all the data that gets written to the xml
type Feature struct {
	Layer *ExportLayer
	Tags  map[string]any
	G     geom.T
}

// Create Ways, Nodes, and Relations for the features
func (f *Feature) AppendToOSM(file *osm.OSM) error {
	switch f.G.(type) {
	case *geom.MultiLineString, *geom.MultiPolygon, *geom.LineString, *geom.Polygon:
	}
	return nil
}

func main() {
	// Define flags using pflag
	pflag.Usage = func() {
		slog.Error(usageHeader, programVersion, os.Args[0])
		pflag.PrintDefaults() // pflag has its own PrintDefaults (will be empty now)
		slog.Error(usageExamples, os.Args[0], os.Args[0], os.Args[0], os.Args[0])
	}

	pflag.Parse() // Parse the flags

	// Process arguments
	args := pflag.Args() // Get non-flag arguments after parsing

	if len(args) < 1 {
		slog.Error("missing input file")
		pflag.Usage()
		os.Exit(1)
	}

	inputGPKG := args[0]
	outputFile := ""

	if len(args) > 1 {
		outputFile = args[1]
		if outputFile != "-" {
			if strings.HasSuffix(strings.ToLower(outputFile), ".pbf") {
			} else {
				slog.Error("invalid output extension. Must be .pbf or .xml", "file", outputFile)
				os.Exit(1)
			}
		}
	}

	// Determine output destination
	var outputWriter *os.File
	if outputFile == "-" {
		outputWriter = os.Stdout
	} else if outputFile != "" {
		// Attempt to create/open the output file
		var err error
		outputWriter, err = os.Create(outputFile)
		if err != nil {
			slog.Error("failed to create output file: %s: %s", outputFile, err)
			os.Exit(1)
		}
		defer outputWriter.Close() // Ensure the file is closed
	}

	// Open GeoPackage database
	db, err := sql.Open("sqlite3", inputGPKG)
	if err != nil {
		slog.Error("failed to open gpkg: %s: %s", inputGPKG, err)
		os.Exit(1)
	}
	defer db.Close()

	// Get layer information including OSM tag mappings
	layers, err := getGeoPackageLayers(db)
	if err != nil {
		slog.Error("error querying layers", "err", err)
		os.Exit(1)
	}

	// Print layer info
	for _, layer := range layers {
		cols := make([]string, len(layer.Tags)+1)
		i := copy(cols, layer.Tags)
		if layer.OSMJsonField {
			cols[i] = "osm_tags"
		}
		slog.Info("found layer for export", slog.String("name", layer.Name), slog.String("cols", strings.Join(cols, ",")), slog.String("geometry", layer.GeometryType))
	}

	// Main logic based on arguments
	if outputFile == "" {
		// Case: prog file.gpkg - Print out columns and fields, no conversion
		slog.Info("no output file specified. exiting")
		return
	}

	pbf, err := osmpbf.NewWriter(context.Background(), outputWriter)
	if err != nil {
		slog.Error("cannot create osmwriter", "error", err)
		os.Exit(10)
	}
	defer pbf.Close()
	// Convert here
	for _, l := range layers {
		results, err := getResults(db, l)
		if err != nil {
			slog.Error("failed to get layer items", "table", l.Name, "err", err)
			continue
		}
		for _, r := range results {
			fmt.Println(r.Layer.GeometryType, r.Tags)
			switch r.G.(type) {
			case *geom.MultiLineString, *geom.MultiPolygon, *geom.LineString, *geom.Polygon:
				e := entity.NewWay(1)
				e.SetGeometry(func() geom.T {
					return r.G
				})
				e.SetTags(r.Tags)
				if err := pbf.WriteEntity(e); err != nil {
					slog.Error("error writing entitiy", "err", err)
				}
			}
		}
	}
}

// Get each feaeture from the given DB and layer. Extract all the OSM tags that we need
func getResults(db *sql.DB, layer *ExportLayer) ([]*Feature, error) {
	res := make([]*Feature, 0, 100)
	rows, err := db.Query(layer.Query())
	if err != nil {
		return nil, err
	}
	fmt.Println(layer.Query())

	for rows.Next() {
		g := &Feature{
			Tags: make(map[string]any),
		}
		var geo []byte
		var osm_tags sql.NullString

		if err := rows.Scan(&geo, &osm_tags); err != nil {
			slog.Error("bad scan for row", "table", layer.Name, "err", err)
			continue
		}

		if !osm_tags.Valid {
			slog.Error("bad row", "table", layer.Name, "err", "no OSM tags data")
			continue
		}
		if len(geo) == 0 {
			slog.Error("bad row", "table", layer.Name, "err", "no geometry data")
			continue
		}

		if err := json.Unmarshal([]byte(osm_tags.String), &g.Tags); err != nil {
			slog.Error("bad osm_tags", "table", layer.Name, "err", err, "data", osm_tags.String)
			continue
		}

		g.G, err = parseGpkgGeom(geo)
		if err != nil {
			slog.Error("bad geo data", "table", layer.Name, "err", err)
			continue
		}
		g.Layer = layer
		n := &osm.Way{
			Tags:    make(osm.Tags, 0, len(g.Tags)),
			Visible: true,
		}
		res = append(res, g)
	}
	return res, nil
}

// getGeoPackageLayers queries the GeoPackage for its feature tables and their column information,
// determining OSM tag mappings based on specific rules.
func getGeoPackageLayers(db *sql.DB) (map[string]*ExportLayer, error) {
	layers := make(map[string]*ExportLayer, 5)
	sqlite_geom_qry := "SELECT table_name, column_name, geometry_type_name, srs_id, z, m FROM gpkg_geometry_columns"

	rows, err := db.Query(sqlite_geom_qry)
	if err != nil {
		return nil, err
	}

	for rows.Next() {
		// The GeoPackage specification defines the columns for gpkg_geometry_columns.
		// These are the common ones, but you might need to adjust based on your specific GeoPackage version/data.
		// Refer to the GeoPackage specification for the exact table schema.
		l := ExportLayer{}
		var geo_type sql.NullString

		err := rows.Scan(
			&l.Name,
			&l.GeometryField,
			&geo_type,
			&l.SRS,
			&l.Z,
			&l.M,
		)

		// Convert the geo_type to the proper enum
		l.GeometryType = geo_type.String
		if err != nil {
			slog.Error("error scanning geometry column", "err", err)
			continue
		}
		layers[l.Name] = &l
	}

	// Now get column information for the layer
	// We only care about layers tag OSM Tag or the "osm_tag" layer that is JSON
	sqlite_col_qry := "SELECT table_name, column_name, description, mime_type FROM gpkg_data_columns"
	rows, err = db.Query(sqlite_col_qry)
	if err != nil {
		return nil, err
	}
	var table, col, desc, mime_type sql.NullString
	for rows.Next() {
		if err := rows.Scan(&table, &col, &desc, &mime_type); err != nil {
			slog.Error("error scanning data column", "error", err)
			continue
		}
		l, ok := layers[table.String]
		if !ok {
			slog.Warn("not a geometry layer", "name", table.String)
			continue
		}

		if col.String == "osm_tags" && mime_type.String == "application/json" {
			// valid OSM tags field
			l.OSMJsonField = true
			continue
		}
		if strings.Contains(strings.ToLower(desc.String), "osm tag") {
			l.Tags = append(l.Tags, col.String)
		}
	}

	// Validate that the layer is exportable
	for name, l := range layers {
		if err := l.Validate(); err != nil {
			slog.Warn("bad layer", "name", name, "reason", err.Error())
			delete(layers, name)
		}
	}
	return layers, nil
}

// Parse the encode geometry from a gpkg
func parseGpkgGeom(data []byte) (geom.T, error) {
	if data[0] != 'G' && data[1] != 'P' {
		return nil, fmt.Errorf("bad header")
	}
	// version := data[2] // Dont care
	// only reading one flag so just pull it
	// https://www.geopackage.org/spec/#gpb_format
	env_size := 0
	switch (data[3] >> 1) & 0b111 {
	case 1:
		env_size = 32
	case 2, 3:
		env_size = 48
	case 4:
		env_size = 64
	default:
		return nil, fmt.Errorf("invalid envelope type: %d", (data[3]>>1)&0b111)
	}
	// skip srs for now
	// skip envelope
	return wkb.Unmarshal(data[8+env_size:])
}
