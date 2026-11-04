# Legacy Office fixtures

Unmodified Apache POI test documents, retrieved from upstream commit
`b8ed3af07928a9196c32551f32f34075d2fc6aeb`.
Source: https://github.com/apache/poi/tree/b8ed3af07928a9196c32551f32f34075d2fc6aeb/test-data

| Local file | Upstream path |
| --- | --- |
| simple.doc | document/simple.doc |
| simple.xls | spreadsheet/Simple.xls |
| multicell.xls | spreadsheet/SimpleMultiCell.xls |
| formulas.xls | spreadsheet/StringFormulas.xls |
| basic.ppt | slideshow/basic_test_ppt_file.ppt |
| macro.doc | document/SimpleMacro.doc |
| macro.xls | spreadsheet/SimpleMacro.xls |

`macro.doc` and `macro.xls` are negative fixtures. Tests inspect their compound
storage and reject them; they never open them in Office or run their macros.
The upstream `legal/LICENSE` and `legal/NOTICE` are retained alongside the fixtures.
