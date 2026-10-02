"""Create a four-page visual review from rendered CAD; render every PDF page for QA."""
from pathlib import Path
from io import BytesIO
import json

import fitz
from PIL import Image
from reportlab.pdfgen import canvas
from reportlab.lib.colors import HexColor
from reportlab.lib.utils import ImageReader
from reportlab.platypus import Paragraph
from reportlab.lib.styles import ParagraphStyle

ROOT=Path(__file__).resolve().parents[2]
CAD=Path(__file__).resolve().parent/'output'
OUT=ROOT/'output/pdf'
QA=ROOT/'tmp/pdfs/minimal-r1-review'
OUT.mkdir(parents=True,exist_ok=True);QA.mkdir(parents=True,exist_ok=True)
PDF=OUT/'minimal-r1-enclosure-review.pdf'
PAGE=(841.89,595.28)
INK='#26373c';MUTED='#53676c';ACCENT='#28786f'
body=ParagraphStyle('body',fontName='Helvetica',fontSize=10,leading=14,textColor=HexColor(MUTED))
c=canvas.Canvas(str(PDF),pagesize=PAGE)
c.setTitle('Minimal R1 | Enclosure visual review')
c.setAuthor('Tesyx')
c.setSubject('Dimensioned CAD views: cover on, cover off, internal layout and exploded assembly')


def paragraph(text,x,y,width=190):
    p=Paragraph(text,body);_,h=p.wrap(width,500);p.drawOn(c,x,y-h);return y-h


def block(title,text,y,color=None):
    x=615
    if color:
        c.setFillColor(HexColor(color));c.roundRect(x,y-10,9,9,2,fill=1,stroke=0)
    c.setFillColor(HexColor(INK));c.setFont('Helvetica-Bold',11)
    c.drawString(x+(16 if color else 0),y-10,title)
    return paragraph(text,x,y-22)-19


def page(n,title,subtitle,image):
    c.setFillColor(HexColor('#ffffff'));c.rect(0,0,*PAGE,fill=1,stroke=0)
    c.setFillColor(HexColor(ACCENT));c.setFont('Helvetica-Bold',9)
    c.drawString(36,561,'TESYX  /  ENCLOSURE REVIEW')
    c.setFillColor(HexColor(INK));c.setFont('Helvetica-Bold',25)
    c.drawString(36,525,title)
    c.setFillColor(HexColor(MUTED));c.setFont('Helvetica',10)
    c.drawString(36,505,subtitle)
    # Crop the render's repeated title/footer; the PDF supplies its own typography.
    im=Image.open(CAD/image).crop((90,145,1710,1310))
    data=BytesIO();im.save(data,format='PNG');data.seek(0)
    c.setFillColor(HexColor('#f4f5f5'));c.roundRect(36,74,549,410,8,fill=1,stroke=0)
    w,h=im.size;scale=min(535/w,396/h)
    c.drawImage(ImageReader(data),36+(549-w*scale)/2,74+(410-h*scale)/2,width=w*scale,height=h*scale)
    c.setStrokeColor(HexColor('#dbe3e3'));c.line(36,48,806,48)
    c.setFillColor(HexColor(MUTED));c.setFont('Helvetica',8)
    c.drawString(36,32,'Minimal R1 | CAD revision 2026-09-14 | Prototype - actual hardware fit not yet verified')
    c.drawRightString(806,32,f'{n} / 4')


page(1,'The minimal enclosure','Cover fitted | Rendered directly from the build123d model','assembled.png')
y=477
y=block('220 x 190 x 84 mm','Outer shell, including the lid. Printed skids and buttons bring the total printed height to 92 mm.',y)
y=block('Simple, serviceable shell','Rounded corners, four recessed lid screws, top exhaust slots and low side inlet slots.',y)
y=block('Replaceable controls','A separate top bezel holds the LCD and two captive buttons. The image shows the aperture; the screen is not illustrated in this closed view.',y)
y=block('First-print construction','ASA shell with removable internal adapters. Rear cable panels and a split clamp let us change the cable without redesigning the enclosure.',y)
paragraph('The footprint grew from the concept sketch to accommodate the selected modules and cable allowances.',615,y)
c.showPage()

page(2,'Inside, with the cover removed','SSD shelf fitted | Coloured blocks show occupied component space','open.png')
y=477
y=block('Pi 4B','Green block, front-left. Mounted on a removable tray with the Pi mounting-hole pattern.',y,'#539879')
y=block('LTE modem + carrier','Blue block, front-right. Quectel EC25-AFX on a Sixfab S121 adapter.',y,'#4b87af')
y=block('Optional power module','Amber block, rear-left. Reserved for supercapacitor evaluation; initially left unpopulated.',y,'#c49458')
y=block('SSD and USB enclosure','Grey block on the raised rear shelf. The removable shelf sits partly above the power bay.',y,'#77818b')
paragraph('Slim dark strips along the sides carry the adhesive antennas. Cables, fasteners and detailed electronics are omitted for clarity.',615,y)
c.showPage()

page(3,'The lower component layout','Top-down view | Cover and SSD shelf hidden to reveal all three lower bays','layout.png')
y=477
y=block('Front = bottom of image','The Pi and modem sit side by side. The optional power bay occupies the rear-left corner.',y)
y=block('Removable adapters','Green: Pi. Blue: LTE. Amber: optional power. Dark plates under the blocks can be replaced independently of the shell.',y)
y=block('Space above the rear bay','The SSD returns on its shelf at Z = 52 mm. Its occupied space starts at Z = 55 mm; the optional power reservation ends at Z = 50 mm.',y)
y=block('Cable allowance','The upper-left volume reserves 48 x 77 x 16 mm for SSD cable loops, plus a separate 30 mm plug allowance. These volumes are not drawn here.',y)
paragraph('Adapter drilling, connector envelopes and flexible cable routing must be checked against the actual purchased parts.',615,y)
c.showPage()

page(4,'How the enclosure comes apart','Exploded CAD view | Upper assembly and SSD shelf lifted for inspection','exploded.png')
y=477
y=block('Upper assembly','The lid lifts with the bezel, LCD retainer and button carrier. Removable signal connectors are needed for servicing.',y)
y=block('Removable SSD shelf','Four screws release the shelf to expose the lower bay. Two straps retain the SSD enclosure.',y)
y=block('20 printable parts','Separate base, lid, bezel, retainers, trays, shelf, cable-clamp pieces, mounting skids and antenna strips.',y)
report=json.loads((CAD/'validation.json').read_text())
assert not any(report[k] for k in ('interferences','proxy_interferences','keepout_interferences'))
y=block('Geometry checked','Printable solids and STL exports passed validation. No overlaps were found among modelled parts, component blocks and cable reservations.',y)
paragraph('This is a mechanical prototype. Detailed hardware fit, thermal behaviour and the buffered electrical connection still need bench verification.',615,y)
c.showPage();c.save()

doc=fitz.open(PDF)
assert len(doc)==4
for i,p in enumerate(doc):
    assert len(p.get_text())>400
    p.get_pixmap(matrix=fitz.Matrix(1.4,1.4),alpha=False).save(QA/f'page-{i+1}.png')
print(PDF)
print(f'Rendered {len(doc)} pages to {QA}')
