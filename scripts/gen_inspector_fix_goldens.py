#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
# Independent FIX field, BodyLength and checksum construction. No bridge imports.
import json
from pathlib import Path
out=[]
for name,side,typ,tif,stp,extra in [('limit',1,2,1,0,{}),('stop-limit-gtd',2,4,7,5,{'stopPrice':'12400','expireDate':'2026-12-31','displayQuantity':2,'stpGroup':'group-a','account':'demo'})]:
 # Protobuf enum numbers are checked against the schema below before use.
 o={'clientOrderId':name,'orderId':name,'symbol':'DEMO','side':side,'quantity':10,'orderType':typ,'timeInForce':tif,'price':'12345','timestamp':'2026-10-26T12:34:56.789Z','stpMode':stp,**extra}
 fields=[(35,'D'),(49,'CONSOLE'),(56,'FXPBRIDGE'),(34,'1'),(52,'20261026-12:34:56.789'),(11,name),(55,'DEMO'),(54,str(side)),(38,'10'),(40,str(typ)),(59,{1:'0',7:'6'}[tif]),(60,'20261026-12:34:56.789'),(44,'123.45')]
 if extra:fields += [(99,'124.00'),(1,'demo'),(432,'20261231'),(111,'2'),(2964,'99'),(2362,'group-a')]
 body=''.join(f'{k}={v}\x01' for k,v in fields);prefix=f'8=FIX.4.4\x019={len(body.encode())}\x01';frame=prefix+body;frame+=f'10={sum(frame.encode())%256:03}\x01'
 out.append({'name':name,'order':o,'fix':frame.replace('\x01','|')})
(Path(__file__).resolve().parent.parent / 'fix_bridge/translator/testdata/order_preview.json').write_text(json.dumps(out,indent=2)+'\n')
