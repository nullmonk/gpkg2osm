# WIP: DOES NOT WORK YET

# gpkg2osm
`gpkg2osm` is a command-line tool written in Go that facilitates the conversion of GeoPackage files (.gpkg) into OpenStreetMap PBF (.osm.pbf) or XML (.osm.xml) formats. It intelligently identifies layers within your GeoPackage, extracts OSM tag mappings, and allows you to export your spatial data into a format widely used by the OpenStreetMap community.

## Usage
```
gpkg2osm v0.1.0
Usage: gpkg2osm [flags] <input.gpkg> [output.osm.pbf|output.osm.xml|-]

Converts a GeoPackage file to an OpenStreetMap PBF or XML file.
Output format is determined by the output file extension (.osm.pbf for PBF, .osm.xml for XML).

Arguments:
  <input.gpkg>       Path to the input GeoPackage file.
  [output.osm.pbf|output.osm.xml|-]   Optional path for the output OSM file.
                     If omitted, the program will print a summary of conversions.
                     Use '-' for stdout (implies XML output).

Flags:
      --help   Show context-sensitive help.

Examples:
  gpkg2osm file.gpkg                           # Print conversion summary (columns/fields) without converting.
  gpkg2osm file.gpkg file.osm.pbf              # Convert file.gpkg to file.osm.pbf.
  gpkg2osm file.gpkg file.osm.xml              # Convert file.gpkg to file.osm.xml.
  gpkg2osm file.gpkg -                         # Convert file.gpkg to OSM XML and print to stdout.
```

## GeoPackage Requirements
For a GeoPackage layer to be considered for export by gpkg2osm, it must meet the following criteria:

* Projection: The layer's Spatial Reference System (SRS) must be EPSG:4326 (WGS 84).
* Geometry Types: Supported geometry types include: POINT, LINESTRING, POLYGON, MULTIPOINT, MULTILINESTRING, and MULTIPOLYGON.
* OSM Tags

### OSM Tags

The layer can contain an osm_tags column of type JSON (MIME type `application/json`) where OSM key-value pairs are stored as a JSON object. This column will be directly used for OSM tags.

Additionally, any column whose description in the gpkg_data_columns table contains the phrase "osm tag" (case-insensitive) will be considered an OSM tag. The column's name will be used as the OSM key, and its value will be the OSM value.

If both osm_tags and descriptive columns are present, the osm_tags JSON will be merged with tags derived from the descriptive columns, with osm_tags taking precedence in case of key conflicts (using json_patch).

## Contributing
Contributions are welcome! If you find a bug or have a feature request, please open an issue on the GitHub repository. Pull requests are also encouraged.

License
This project is licensed under the MIT License.