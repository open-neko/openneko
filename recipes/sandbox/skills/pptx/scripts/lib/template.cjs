"use strict";

const NS = {
  a: "http://schemas.openxmlformats.org/drawingml/2006/main",
  r: "http://schemas.openxmlformats.org/officeDocument/2006/relationships",
  p: "http://schemas.openxmlformats.org/presentationml/2006/main",
  c: "http://schemas.openxmlformats.org/drawingml/2006/chart",
  rel: "http://schemas.openxmlformats.org/package/2006/relationships",
  ct: "http://schemas.openxmlformats.org/package/2006/content-types",
};

const REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
const REL_TYPES = {
  document: `${REL}/officeDocument`,
  slide: `${REL}/slide`,
  slideLayout: `${REL}/slideLayout`,
  slideMaster: `${REL}/slideMaster`,
  notesSlide: `${REL}/notesSlide`,
  notesMaster: `${REL}/notesMaster`,
  theme: `${REL}/theme`,
  presProps: `${REL}/presProps`,
  tableStyles: `${REL}/tableStyles`,
  image: `${REL}/image`,
  hyperlink: `${REL}/hyperlink`,
  chart: `${REL}/chart`,
  package: `${REL}/package`,
  vbaProject: "http://schemas.microsoft.com/office/2006/relationships/vbaProject",
  core: "http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties",
  app: `${REL}/extended-properties`,
};

const PML = "application/vnd.openxmlformats-officedocument.presentationml";
const CONTENT_TYPES = {
  presentation: `${PML}.presentation.main+xml`,
  template: `${PML}.template.main+xml`,
  macro: "application/vnd.ms-powerpoint.presentation.macroEnabled.main+xml",
  macroTemplate: "application/vnd.ms-powerpoint.template.macroEnabled.main+xml",
  slideshow: `${PML}.slideshow.main+xml`,
  slide: `${PML}.slide+xml`,
  slideLayout: `${PML}.slideLayout+xml`,
  slideMaster: `${PML}.slideMaster+xml`,
  notesSlide: `${PML}.notesSlide+xml`,
  notesMaster: `${PML}.notesMaster+xml`,
  presProps: `${PML}.presProps+xml`,
  tableStyles: `${PML}.tableStyles+xml`,
  theme: "application/vnd.openxmlformats-officedocument.theme+xml",
  chart: "application/vnd.openxmlformats-officedocument.drawingml.chart+xml",
  xlsx: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  core: "application/vnd.openxmlformats-package.core-properties+xml",
  app: "application/vnd.openxmlformats-officedocument.extended-properties+xml",
};

const IMAGE_TYPES = { png: "image/png", jpeg: "image/jpeg", gif: "image/gif" };

const HEAD = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n';
const ROOT_NS = `xmlns:a="${NS.a}" xmlns:r="${NS.r}" xmlns:p="${NS.p}"`;
const EMU_PER_CM = 360000;
const TABLE_STYLE = "{5C22544A-7EE6-4342-B048-85BDC9FD1C3A}";

const SLIDE_SIZES = {
  "16:9": [12192000, 6858000],
  "4:3": [9144000, 6858000],
  "16:10": [10972800, 6858000],
};

const GROUP_HEAD = '<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>';
const CLR_MAP = 'bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"';
const MASTER_MAPPING = "<p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr>";

function font(slot, size, extra = "") {
  return `<a:defRPr sz="${size}" kern="1200"${extra}><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+${slot}-lt"/><a:ea typeface="+${slot}-ea"/><a:cs typeface="+${slot}-cs"/></a:defRPr>`;
}

function levels(count, make) {
  let out = "";
  for (let i = 1; i <= count; i++) out += make(i);
  return out;
}

const THEME = `${HEAD}<a:theme xmlns:a="${NS.a}" name="OpenNeko"><a:themeElements><a:clrScheme name="OpenNeko"><a:dk1><a:sysClr val="windowText" lastClr="000000"/></a:dk1><a:lt1><a:sysClr val="window" lastClr="FFFFFF"/></a:lt1><a:dk2><a:srgbClr val="1F2937"/></a:dk2><a:lt2><a:srgbClr val="F3F4F6"/></a:lt2><a:accent1><a:srgbClr val="2563EB"/></a:accent1><a:accent2><a:srgbClr val="F59E0B"/></a:accent2><a:accent3><a:srgbClr val="10B981"/></a:accent3><a:accent4><a:srgbClr val="EF4444"/></a:accent4><a:accent5><a:srgbClr val="8B5CF6"/></a:accent5><a:accent6><a:srgbClr val="64748B"/></a:accent6><a:hlink><a:srgbClr val="2563EB"/></a:hlink><a:folHlink><a:srgbClr val="7C3AED"/></a:folHlink></a:clrScheme><a:fontScheme name="OpenNeko"><a:majorFont><a:latin typeface="Calibri"/><a:ea typeface=""/><a:cs typeface=""/></a:majorFont><a:minorFont><a:latin typeface="Calibri"/><a:ea typeface=""/><a:cs typeface=""/></a:minorFont></a:fontScheme><a:fmtScheme name="OpenNeko"><a:fillStyleLst>${'<a:solidFill><a:schemeClr val="phClr"/></a:solidFill>'.repeat(3)}</a:fillStyleLst><a:lnStyleLst>${[6350, 12700, 19050].map((w) => `<a:ln w="${w}" cap="flat" cmpd="sng" algn="ctr"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:prstDash val="solid"/><a:miter lim="800000"/></a:ln>`).join("")}</a:lnStyleLst><a:effectStyleLst>${"<a:effectStyle><a:effectLst/></a:effectStyle>".repeat(3)}</a:effectStyleLst><a:bgFillStyleLst>${'<a:solidFill><a:schemeClr val="phClr"/></a:solidFill>'.repeat(3)}</a:bgFillStyleLst></a:fmtScheme></a:themeElements><a:objectDefaults/><a:extraClrSchemeLst/></a:theme>`;

function xfrm(box) {
  return `<a:xfrm><a:off x="${Math.round(box[0])}" y="${Math.round(box[1])}"/><a:ext cx="${Math.round(box[2])}" cy="${Math.round(box[3])}"/></a:xfrm>`;
}

function phShape(id, name, ph, box, body = "", bodyPr = "<a:bodyPr/>", prompt = "") {
  const geometry = box ? `<p:spPr>${xfrm(box)}<a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>` : "<p:spPr/>";
  const text = prompt ? `<a:p><a:r><a:rPr lang="en-US"/><a:t>${prompt}</a:t></a:r></a:p>` : '<a:p><a:endParaRPr lang="en-US"/></a:p>';
  return `<p:sp><p:nvSpPr><p:cNvPr id="${id}" name="${name}"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph ${ph}/></p:nvPr></p:nvSpPr>${geometry}<p:txBody>${bodyPr}<a:lstStyle>${body}</a:lstStyle>${text}</p:txBody></p:sp>`;
}

function geometry(cx, cy) {
  const mx = cx * 0.06875;
  const width = cx - 2 * mx;
  return {
    title: [mx, cy * 0.0532, width, cy * 0.1933],
    body: [mx, cy * 0.2662, width, cy * 0.6345],
    date: [mx, cy * 0.9268, width * 0.26, cy * 0.0532],
    footer: [mx + width * 0.305, cy * 0.9268, width * 0.39, cy * 0.0532],
    number: [mx + width * 0.74, cy * 0.9268, width * 0.26, cy * 0.0532],
    centerTitle: [cx * 0.125, cy * 0.1637, cx * 0.75, cy * 0.3483],
    centerSub: [cx * 0.125, cy * 0.5254, cx * 0.75, cy * 0.2416],
    sectionTitle: [mx, cy * 0.2569, width, cy * 0.4288],
    sectionBody: [mx, cy * 0.6897, width, cy * 0.2255],
    left: [mx, cy * 0.2662, width * 0.485, cy * 0.6345],
    right: [mx + width * 0.515, cy * 0.2662, width * 0.485, cy * 0.6345],
  };
}

const BODY_SIZES = [2800, 2400, 2000, 1800, 1800];

function masterXml(cx, cy, layoutCount) {
  const g = geometry(cx, cy);
  const small = `<a:lvl1pPr algn="l">${font("mn", 1200, ' b="0"').replace('<a:schemeClr val="tx1"/>', '<a:schemeClr val="tx1"><a:tint val="75000"/></a:schemeClr>')}</a:lvl1pPr>`;
  const shapes = [
    phShape(2, "Title Placeholder 1", 'type="title"', g.title, "", '<a:bodyPr vert="horz" lIns="91440" tIns="45720" rIns="91440" bIns="45720" rtlCol="0" anchor="ctr"><a:normAutofit/></a:bodyPr>', "Click to edit the title"),
    phShape(3, "Text Placeholder 2", 'type="body" idx="1"', g.body, "", '<a:bodyPr vert="horz" lIns="91440" tIns="45720" rIns="91440" bIns="45720" rtlCol="0"><a:normAutofit/></a:bodyPr>', "Click to edit the text"),
    phShape(4, "Date Placeholder 3", 'type="dt" sz="half" idx="2"', g.date, small, '<a:bodyPr vert="horz" lIns="91440" tIns="45720" rIns="91440" bIns="45720" rtlCol="0" anchor="ctr"/>'),
    phShape(5, "Footer Placeholder 4", 'type="ftr" sz="quarter" idx="3"', g.footer, small.replace('algn="l"', 'algn="ctr"'), '<a:bodyPr vert="horz" lIns="91440" tIns="45720" rIns="91440" bIns="45720" rtlCol="0" anchor="ctr"/>'),
    phShape(6, "Slide Number Placeholder 5", 'type="sldNum" sz="quarter" idx="4"', g.number, small.replace('algn="l"', 'algn="r"'), '<a:bodyPr vert="horz" lIns="91440" tIns="45720" rIns="91440" bIns="45720" rtlCol="0" anchor="ctr"/>'),
  ].join("");
  const layoutIds = levels(layoutCount, (i) => `<p:sldLayoutId id="${2147483648 + i}" r:id="rId${i}"/>`);
  const title = `<p:titleStyle><a:lvl1pPr algn="l" defTabSz="914400" rtl="0" eaLnBrk="1" latinLnBrk="0" hangingPunct="1"><a:lnSpc><a:spcPct val="90000"/></a:lnSpc><a:spcBef><a:spcPct val="0"/></a:spcBef><a:buNone/>${font("mj", 4400)}</a:lvl1pPr></p:titleStyle>`;
  const body = `<p:bodyStyle>${levels(5, (i) => `<a:lvl${i}pPr marL="${228600 + (i - 1) * 457200}" indent="-228600" algn="l" defTabSz="914400" rtl="0" eaLnBrk="1" latinLnBrk="0" hangingPunct="1"><a:lnSpc><a:spcPct val="90000"/></a:lnSpc><a:spcBef><a:spcPts val="${i === 1 ? 1000 : 500}"/></a:spcBef><a:buFont typeface="Arial"/><a:buChar char="${i % 2 ? "•" : "–"}"/>${font("mn", BODY_SIZES[i - 1])}</a:lvl${i}pPr>`)}</p:bodyStyle>`;
  const other = `<p:otherStyle><a:defPPr><a:defRPr lang="en-US"/></a:defPPr>${levels(5, (i) => `<a:lvl${i}pPr marL="${(i - 1) * 457200}" algn="l" defTabSz="914400" rtl="0" eaLnBrk="1" latinLnBrk="0" hangingPunct="1">${font("mn", 1800)}</a:lvl${i}pPr>`)}</p:otherStyle>`;
  return `${HEAD}<p:sldMaster ${ROOT_NS}><p:cSld><p:bg><p:bgRef idx="1001"><a:schemeClr val="bg1"/></p:bgRef></p:bg><p:spTree>${GROUP_HEAD}${shapes}</p:spTree></p:cSld><p:clrMap ${CLR_MAP}/><p:sldLayoutIdLst>${layoutIds}</p:sldLayoutIdLst><p:txStyles>${title}${body}${other}</p:txStyles></p:sldMaster>`;
}

function footerShapes(first) {
  return [
    phShape(first, "Date Placeholder", 'type="dt" sz="half" idx="10"', null),
    phShape(first + 1, "Footer Placeholder", 'type="ftr" sz="quarter" idx="11"', null),
    phShape(first + 2, "Slide Number Placeholder", 'type="sldNum" sz="quarter" idx="12"', null),
  ];
}

function layoutDefs(cx, cy) {
  const g = geometry(cx, cy);
  const centered = (size) => `<a:lvl1pPr algn="ctr"><a:defRPr sz="${size}"/></a:lvl1pPr>`;
  const plain = (size) => `<a:lvl1pPr marL="0" indent="0"><a:buNone/><a:defRPr sz="${size}"/></a:lvl1pPr>`;
  const subtitle = `<a:lvl1pPr marL="0" indent="0" algn="ctr"><a:buNone/><a:defRPr sz="2400"/></a:lvl1pPr>`;
  return [
    { name: "Title Slide", type: "title", shapes: [phShape(2, "Title 1", 'type="ctrTitle"', g.centerTitle, centered(6000), '<a:bodyPr anchor="b"/>', "Click to add a title"), phShape(3, "Subtitle 2", 'type="subTitle" idx="1"', g.centerSub, subtitle, "<a:bodyPr/>", "Click to add a subtitle")] },
    { name: "Title and Content", type: "obj", shapes: [phShape(2, "Title 1", 'type="title"', null, "", "<a:bodyPr/>", "Click to add a title"), phShape(3, "Content Placeholder 2", 'idx="1"', null, "", "<a:bodyPr/>", "Click to add text")] },
    { name: "Section Header", type: "secHead", shapes: [phShape(2, "Title 1", 'type="title"', g.sectionTitle, '<a:lvl1pPr><a:defRPr sz="6000"/></a:lvl1pPr>', '<a:bodyPr anchor="b"/>', "Click to add a title"), phShape(3, "Text Placeholder 2", 'type="body" idx="1"', g.sectionBody, plain(2400), "<a:bodyPr/>", "Click to add text")] },
    { name: "Two Content", type: "twoObj", shapes: [phShape(2, "Title 1", 'type="title"', null, "", "<a:bodyPr/>", "Click to add a title"), phShape(3, "Content Placeholder 2", 'sz="half" idx="1"', g.left, "", "<a:bodyPr/>", "Click to add text"), phShape(4, "Content Placeholder 3", 'sz="half" idx="2"', g.right, "", "<a:bodyPr/>", "Click to add text")] },
    { name: "Title Only", type: "titleOnly", shapes: [phShape(2, "Title 1", 'type="title"', null, "", "<a:bodyPr/>", "Click to add a title")] },
    { name: "Blank", type: "blank", shapes: [] },
  ];
}

function layoutXml(def) {
  const shapes = [...def.shapes, ...footerShapes(def.shapes.length + 2)];
  return `${HEAD}<p:sldLayout ${ROOT_NS} type="${def.type}" preserve="1"><p:cSld name="${def.name}"><p:spTree>${GROUP_HEAD}${shapes.join("")}</p:spTree></p:cSld>${MASTER_MAPPING}</p:sldLayout>`;
}

function presentationXml(cx, cy, layoutCount) {
  const type = cx === 9144000 && cy === 6858000 ? ' type="screen4x3"' : "";
  const text = levels(5, (i) => `<a:lvl${i}pPr marL="${(i - 1) * 457200}" algn="l" defTabSz="914400" rtl="0" eaLnBrk="1" latinLnBrk="0" hangingPunct="1">${font("mn", 1800)}</a:lvl${i}pPr>`);
  return `${HEAD}<p:presentation ${ROOT_NS} saveSubsetFonts="1"><p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst><p:sldIdLst/><p:sldSz cx="${cx}" cy="${cy}"${type}/><p:notesSz cx="6858000" cy="9144000"/><p:defaultTextStyle><a:defPPr><a:defRPr lang="en-US"/></a:defPPr>${text}</p:defaultTextStyle></p:presentation>`;
}

function blankParts(cx, cy) {
  const layouts = layoutDefs(cx, cy);
  const files = {
    "[Content_Types].xml": `${HEAD}<Types xmlns="${NS.ct}"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/ppt/presentation.xml" ContentType="${CONTENT_TYPES.presentation}"/><Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="${CONTENT_TYPES.slideMaster}"/>${layouts.map((_, i) => `<Override PartName="/ppt/slideLayouts/slideLayout${i + 1}.xml" ContentType="${CONTENT_TYPES.slideLayout}"/>`).join("")}<Override PartName="/ppt/theme/theme1.xml" ContentType="${CONTENT_TYPES.theme}"/><Override PartName="/ppt/presProps.xml" ContentType="${CONTENT_TYPES.presProps}"/><Override PartName="/ppt/tableStyles.xml" ContentType="${CONTENT_TYPES.tableStyles}"/><Override PartName="/docProps/core.xml" ContentType="${CONTENT_TYPES.core}"/><Override PartName="/docProps/app.xml" ContentType="${CONTENT_TYPES.app}"/></Types>`,
    "_rels/.rels": `${HEAD}<Relationships xmlns="${NS.rel}"><Relationship Id="rId1" Type="${REL_TYPES.document}" Target="ppt/presentation.xml"/><Relationship Id="rId2" Type="${REL_TYPES.core}" Target="docProps/core.xml"/><Relationship Id="rId3" Type="${REL_TYPES.app}" Target="docProps/app.xml"/></Relationships>`,
    "docProps/core.xml": `${HEAD}<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"></cp:coreProperties>`,
    "docProps/app.xml": `${HEAD}<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>OpenNeko</Application></Properties>`,
    "ppt/presentation.xml": presentationXml(cx, cy, layouts.length),
    "ppt/_rels/presentation.xml.rels": `${HEAD}<Relationships xmlns="${NS.rel}"><Relationship Id="rId1" Type="${REL_TYPES.slideMaster}" Target="slideMasters/slideMaster1.xml"/><Relationship Id="rId2" Type="${REL_TYPES.theme}" Target="theme/theme1.xml"/><Relationship Id="rId3" Type="${REL_TYPES.presProps}" Target="presProps.xml"/><Relationship Id="rId4" Type="${REL_TYPES.tableStyles}" Target="tableStyles.xml"/></Relationships>`,
    "ppt/presProps.xml": `${HEAD}<p:presentationPr ${ROOT_NS}/>`,
    "ppt/tableStyles.xml": `${HEAD}<a:tblStyleLst xmlns:a="${NS.a}" def="${TABLE_STYLE}"/>`,
    "ppt/theme/theme1.xml": THEME,
    "ppt/slideMasters/slideMaster1.xml": masterXml(cx, cy, layouts.length),
    "ppt/slideMasters/_rels/slideMaster1.xml.rels": `${HEAD}<Relationships xmlns="${NS.rel}">${layouts.map((_, i) => `<Relationship Id="rId${i + 1}" Type="${REL_TYPES.slideLayout}" Target="../slideLayouts/slideLayout${i + 1}.xml"/>`).join("")}<Relationship Id="rId${layouts.length + 1}" Type="${REL_TYPES.theme}" Target="../theme/theme1.xml"/></Relationships>`,
  };
  layouts.forEach((def, i) => {
    files[`ppt/slideLayouts/slideLayout${i + 1}.xml`] = layoutXml(def);
    files[`ppt/slideLayouts/_rels/slideLayout${i + 1}.xml.rels`] = `${HEAD}<Relationships xmlns="${NS.rel}"><Relationship Id="rId1" Type="${REL_TYPES.slideMaster}" Target="../slideMasters/slideMaster1.xml"/></Relationships>`;
  });
  return files;
}

const NOTES_MASTER = `${HEAD}<p:notesMaster ${ROOT_NS}><p:cSld><p:bg><p:bgRef idx="1001"><a:schemeClr val="bg1"/></p:bgRef></p:bg><p:spTree>${GROUP_HEAD}${phShape(2, "Slide Image Placeholder 1", 'type="sldImg" idx="2"', [685800, 1143000, 5486400, 3086100], "", "<a:bodyPr/>").replace(/<p:txBody>.*<\/p:txBody>/, "").replace('<a:spLocks noGrp="1"/>', '<a:spLocks noGrp="1" noRot="1" noChangeAspect="1"/>')}${phShape(3, "Notes Placeholder 2", 'type="body" sz="quarter" idx="3"', [685800, 4400550, 5486400, 3600450], "", "<a:bodyPr/>")}</p:spTree></p:cSld><p:clrMap ${CLR_MAP}/><p:notesStyle>${levels(5, (i) => `<a:lvl${i}pPr marL="${(i - 1) * 457200}" algn="l" defTabSz="914400" rtl="0" eaLnBrk="1" latinLnBrk="0" hangingPunct="1">${font("mn", 1200)}</a:lvl${i}pPr>`)}</p:notesStyle></p:notesMaster>`;

function notesSlideXml(text) {
  return `${HEAD}<p:notes ${ROOT_NS}><p:cSld><p:spTree>${GROUP_HEAD}<p:sp><p:nvSpPr><p:cNvPr id="2" name="Slide Image Placeholder 1"/><p:cNvSpPr><a:spLocks noGrp="1" noRot="1" noChangeAspect="1"/></p:cNvSpPr><p:nvPr><p:ph type="sldImg"/></p:nvPr></p:nvSpPr><p:spPr/></p:sp><p:sp><p:nvSpPr><p:cNvPr id="3" name="Notes Placeholder 2"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="body" idx="1"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/>${text}</p:txBody></p:sp></p:spTree></p:cSld>${MASTER_MAPPING}</p:notes>`;
}

function slideXml() {
  return `${HEAD}<p:sld ${ROOT_NS}><p:cSld><p:spTree>${GROUP_HEAD}</p:spTree></p:cSld>${MASTER_MAPPING}</p:sld>`;
}

module.exports = {
  BODY_SIZES,
  REL,
  CONTENT_TYPES,
  EMU_PER_CM,
  HEAD,
  IMAGE_TYPES,
  NOTES_MASTER,
  NS,
  REL_TYPES,
  ROOT_NS,
  SLIDE_SIZES,
  TABLE_STYLE,
  THEME,
  blankParts,
  notesSlideXml,
  slideXml,
};
