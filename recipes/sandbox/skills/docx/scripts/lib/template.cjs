"use strict";

const NS = {
  w: "http://schemas.openxmlformats.org/wordprocessingml/2006/main",
  r: "http://schemas.openxmlformats.org/officeDocument/2006/relationships",
  wp: "http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing",
  a: "http://schemas.openxmlformats.org/drawingml/2006/main",
  pic: "http://schemas.openxmlformats.org/drawingml/2006/picture",
  rel: "http://schemas.openxmlformats.org/package/2006/relationships",
  ct: "http://schemas.openxmlformats.org/package/2006/content-types",
};

const REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
const REL_TYPES = {
  document: `${REL}/officeDocument`,
  styles: `${REL}/styles`,
  numbering: `${REL}/numbering`,
  settings: `${REL}/settings`,
  comments: `${REL}/comments`,
  header: `${REL}/header`,
  footer: `${REL}/footer`,
  image: `${REL}/image`,
  hyperlink: `${REL}/hyperlink`,
  core: "http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties",
  app: `${REL}/extended-properties`,
};

const WML = "application/vnd.openxmlformats-officedocument.wordprocessingml";
const CONTENT_TYPES = {
  document: `${WML}.document.main+xml`,
  macroDocument: "application/vnd.ms-word.document.macroEnabled.main+xml",
  styles: `${WML}.styles+xml`,
  numbering: `${WML}.numbering+xml`,
  settings: `${WML}.settings+xml`,
  comments: `${WML}.comments+xml`,
  header: `${WML}.header+xml`,
  footer: `${WML}.footer+xml`,
  core: "application/vnd.openxmlformats-package.core-properties+xml",
  app: "application/vnd.openxmlformats-officedocument.extended-properties+xml",
};

const IMAGE_TYPES = { png: "image/png", jpeg: "image/jpeg", gif: "image/gif" };

const HEAD = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n';
const ROOT_NS = `xmlns:w="${NS.w}" xmlns:r="${NS.r}" xmlns:wp="${NS.wp}" xmlns:a="${NS.a}" xmlns:pic="${NS.pic}"`;

const HEADING_SIZES = [32, 26, 24, 22, 22, 22];

const STYLE_DEFS = {
  normal: '<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:after="160" w:line="259" w:lineRule="auto"/></w:pPr></w:style>',
  "default paragraph font": '<w:style w:type="character" w:default="1" w:styleId="DefaultParagraphFont"><w:name w:val="Default Paragraph Font"/><w:uiPriority w:val="1"/><w:semiHidden/><w:unhideWhenUsed/></w:style>',
  "normal table": '<w:style w:type="table" w:default="1" w:styleId="TableNormal"><w:name w:val="Normal Table"/><w:uiPriority w:val="99"/><w:semiHidden/><w:unhideWhenUsed/><w:tblPr><w:tblInd w:w="0" w:type="dxa"/><w:tblCellMar><w:top w:w="0" w:type="dxa"/><w:left w:w="108" w:type="dxa"/><w:bottom w:w="0" w:type="dxa"/><w:right w:w="108" w:type="dxa"/></w:tblCellMar></w:tblPr></w:style>',
  title: '<w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:after="80" w:line="240" w:lineRule="auto"/><w:contextualSpacing/></w:pPr><w:rPr><w:spacing w:val="-10"/><w:kern w:val="28"/><w:sz w:val="56"/><w:szCs w:val="56"/></w:rPr></w:style>',
  subtitle: '<w:style w:type="paragraph" w:styleId="Subtitle"><w:name w:val="Subtitle"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:rPr><w:color w:val="595959"/><w:spacing w:val="15"/><w:sz w:val="28"/><w:szCs w:val="28"/></w:rPr></w:style>',
  "list paragraph": '<w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/><w:basedOn w:val="Normal"/><w:uiPriority w:val="34"/><w:qFormat/><w:pPr><w:ind w:left="720"/><w:contextualSpacing/></w:pPr></w:style>',
  quote: '<w:style w:type="paragraph" w:styleId="Quote"><w:name w:val="Quote"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:uiPriority w:val="29"/><w:qFormat/><w:pPr><w:spacing w:before="160"/><w:ind w:left="864" w:right="864"/><w:jc w:val="center"/></w:pPr><w:rPr><w:i/><w:iCs/><w:color w:val="404040"/></w:rPr></w:style>',
  caption: '<w:style w:type="paragraph" w:styleId="Caption"><w:name w:val="caption"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:uiPriority w:val="35"/><w:unhideWhenUsed/><w:qFormat/><w:pPr><w:spacing w:after="200" w:line="240" w:lineRule="auto"/></w:pPr><w:rPr><w:i/><w:iCs/><w:color w:val="44546A"/><w:sz w:val="18"/><w:szCs w:val="18"/></w:rPr></w:style>',
  "table grid": '<w:style w:type="table" w:styleId="TableGrid"><w:name w:val="Table Grid"/><w:basedOn w:val="TableNormal"/><w:uiPriority w:val="39"/><w:pPr><w:spacing w:after="0" w:line="240" w:lineRule="auto"/></w:pPr><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:left w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:bottom w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:right w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:insideH w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:insideV w:val="single" w:sz="4" w:space="0" w:color="auto"/></w:tblBorders></w:tblPr></w:style>',
  hyperlink: '<w:style w:type="character" w:styleId="Hyperlink"><w:name w:val="Hyperlink"/><w:basedOn w:val="DefaultParagraphFont"/><w:uiPriority w:val="99"/><w:unhideWhenUsed/><w:rPr><w:color w:val="0563C1"/><w:u w:val="single"/></w:rPr></w:style>',
  "toc heading": '<w:style w:type="paragraph" w:styleId="TOCHeading"><w:name w:val="TOC Heading"/><w:basedOn w:val="Heading1"/><w:next w:val="Normal"/><w:uiPriority w:val="39"/><w:unhideWhenUsed/><w:qFormat/><w:pPr><w:outlineLvl w:val="9"/></w:pPr></w:style>',
};
for (let level = 1; level <= 6; level++) {
  const size = HEADING_SIZES[level - 1];
  STYLE_DEFS[`heading ${level}`] = `<w:style w:type="paragraph" w:styleId="Heading${level}"><w:name w:val="heading ${level}"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:uiPriority w:val="9"/><w:qFormat/><w:pPr><w:keepNext/><w:keepLines/><w:spacing w:before="${level === 1 ? 360 : 160}" w:after="80"/><w:outlineLvl w:val="${level - 1}"/></w:pPr><w:rPr><w:b/><w:bCs/><w:color w:val="1F3864"/><w:sz w:val="${size}"/><w:szCs w:val="${size}"/></w:rPr></w:style>`;
}

const BULLETS = ["•", "◦", "▪"];
const NUMBER_FORMATS = ["decimal", "lowerLetter", "lowerRoman"];

function abstractNumXml(id, kind) {
  const levels = [];
  for (let level = 0; level < 9; level++) {
    const indent = 720 * (level + 1);
    const text = kind === "bullet" ? BULLETS[level % 3] : `%${level + 1}.`;
    const format = kind === "bullet" ? "bullet" : NUMBER_FORMATS[level % 3];
    const font = kind === "bullet" ? '<w:rPr><w:rFonts w:ascii="Arial" w:hAnsi="Arial" w:hint="default"/></w:rPr>' : "";
    levels.push(`<w:lvl w:ilvl="${level}"><w:start w:val="1"/><w:numFmt w:val="${format}"/><w:lvlText w:val="${text}"/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="${indent}" w:hanging="360"/></w:pPr>${font}</w:lvl>`);
  }
  const name = kind === "bullet" ? "OpenNeko bullets" : "OpenNeko numbers";
  return `<w:abstractNum w:abstractNumId="${id}"><w:multiLevelType w:val="hybridMultilevel"/><w:name w:val="${name}"/>${levels.join("")}</w:abstractNum>`;
}

function stylesXml(font) {
  const name = font?.name ?? "Calibri";
  const size = Math.round((font?.size ?? 11) * 2);
  const defs = ["normal", "default paragraph font", "normal table", "title", "subtitle", "heading 1", "heading 2", "heading 3", "list paragraph", "quote", "caption", "table grid", "hyperlink"];
  return `${HEAD}<w:styles ${ROOT_NS}><w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="${escapeAttr(name)}" w:hAnsi="${escapeAttr(name)}" w:eastAsia="${escapeAttr(name)}" w:cs="${escapeAttr(name)}"/><w:sz w:val="${size}"/><w:szCs w:val="${size}"/><w:lang w:val="en-US" w:eastAsia="en-US" w:bidi="ar-SA"/></w:rPr></w:rPrDefault><w:pPrDefault/></w:docDefaults>${defs.map((key) => STYLE_DEFS[key]).join("")}</w:styles>`;
}

function escapeAttr(value) {
  return String(value).replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;");
}

function escapeText(value) {
  return String(value).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function blankPackage({ title, author, font, date }) {
  const stamp = date ?? new Date().toISOString().replace(/\.\d+Z$/, "Z");
  const creator = author ?? "OpenNeko";
  return {
    "[Content_Types].xml": `${HEAD}<Types xmlns="${NS.ct}"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="${CONTENT_TYPES.document}"/><Override PartName="/word/styles.xml" ContentType="${CONTENT_TYPES.styles}"/><Override PartName="/word/settings.xml" ContentType="${CONTENT_TYPES.settings}"/><Override PartName="/docProps/core.xml" ContentType="${CONTENT_TYPES.core}"/><Override PartName="/docProps/app.xml" ContentType="${CONTENT_TYPES.app}"/></Types>`,
    "_rels/.rels": `${HEAD}<Relationships xmlns="${NS.rel}"><Relationship Id="rId1" Type="${REL_TYPES.document}" Target="word/document.xml"/><Relationship Id="rId2" Type="${REL_TYPES.core}" Target="docProps/core.xml"/><Relationship Id="rId3" Type="${REL_TYPES.app}" Target="docProps/app.xml"/></Relationships>`,
    "word/_rels/document.xml.rels": `${HEAD}<Relationships xmlns="${NS.rel}"><Relationship Id="rId1" Type="${REL_TYPES.styles}" Target="styles.xml"/><Relationship Id="rId2" Type="${REL_TYPES.settings}" Target="settings.xml"/></Relationships>`,
    "word/document.xml": `${HEAD}<w:document ${ROOT_NS}><w:body><w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="708" w:footer="708" w:gutter="0"/></w:sectPr></w:body></w:document>`,
    "word/styles.xml": stylesXml(font),
    "word/settings.xml": `${HEAD}<w:settings ${ROOT_NS}><w:defaultTabStop w:val="720"/><w:characterSpacingControl w:val="doNotCompress"/><w:compat><w:compatSetting w:name="compatibilityMode" w:uri="http://schemas.microsoft.com/office/word" w:val="15"/></w:compat></w:settings>`,
    "docProps/core.xml": `${HEAD}<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dc:title>${escapeText(title ?? "")}</dc:title><dc:creator>${escapeText(creator)}</dc:creator><cp:lastModifiedBy>${escapeText(creator)}</cp:lastModifiedBy><dcterms:created xsi:type="dcterms:W3CDTF">${stamp}</dcterms:created><dcterms:modified xsi:type="dcterms:W3CDTF">${stamp}</dcterms:modified></cp:coreProperties>`,
    "docProps/app.xml": `${HEAD}<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>OpenNeko</Application></Properties>`,
  };
}

const PAGE_SIZES = {
  a4: [11906, 16838],
  letter: [12240, 15840],
  legal: [12240, 20160],
  a5: [8391, 11906],
  a3: [16838, 23811],
};

module.exports = {
  CONTENT_TYPES,
  HEAD,
  IMAGE_TYPES,
  NS,
  PAGE_SIZES,
  REL_TYPES,
  ROOT_NS,
  STYLE_DEFS,
  abstractNumXml,
  blankPackage,
  escapeAttr,
  escapeText,
};
