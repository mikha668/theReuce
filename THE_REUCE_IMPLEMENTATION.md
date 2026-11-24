# theReuce implementationsuppdrag

**Till kodagenten.** Läs denna fil, repots instruktioner och berörd kod. Implementera etapp 1 till 4 i ordning i det befintliga Go-, PostgreSQL- och React-repot. Gör ändringarna i kod, migrationer, OpenAPI, UI och tester. Använd den faktiska checkouten för filnamn och migrationsnummer. Rapportera arbete som genomfört först när relevanta tester har körts. Det här är det samlade uppdraget för denna leverans.

## 1. Mål och bindande regler

Leverera ett flöde där en kassa registrerar ett köp, ett lager kan komplettera med fysiska exemplar och rätt användare kan se sina produkter och köp. Partnern ska kunna läsa orderstatus. Köparen ska kunna öppna kvitto, individuell garanti och registrerade DPP-länkar där tillgång finns. Admin ska kunna utreda väntande kopplingar. En separat API-konsol ska kunna köra flödet med testdata.

1. **Köp kan sparas med noll orderrader och noll exemplar.** Identifiering av köparen och identifiering av exemplaret kan ske i valfri ordning.
2. **Godkänd första tilldelning stämplar det fysiska exemplaret.** Spara ägarens stabila interna part-ID i `product_instances.owner_party_id`. Skriv stämpel, ägarhändelse, audit och outbox i samma PostgreSQL-transaktion. En part kan vara person eller organisation. Ett konto, alias eller partnerlokalt ID får aldrig användas som stämpel.
3. **Ägarstämpeln bestämmer vem som har produkten i Mina produkter.** Uppstart, inloggning, läsningar och aliasändringar får aldrig tilldela eller flytta ägande. Ett vanligt partneranrop får aldrig ändra en befintlig stämpel. Ett framtida ägarbyte, retur eller rättelse kräver en särskild operation med mandat och historik.
4. **Rapporterad köpare och godkänd köparkoppling är två skilda uppgifter.** Spara godkänd köpare på köpet. Ett nytt alias eller kontroll av en e-postadress idag ger ingen automatisk tillgång till ett äldre köp.
5. **Behåll befintliga partneroperationer.** Bevara `/partner/v1/orders`, `order_ref`, `event_ref` och historiska idempotenta svar.  Ägar- och åtkomstreglerna ovan gäller även äldre klienters nya anrop.
6. **Bygg vidare på monorepot och transaktionell outbox.** För den här leveransen räcker ett Go-API, PostgreSQL, befintlig React-frontend, en separat frontend-entrypoint för sandlådan och privat objektlagring för kvitton.

Lägg ägarregeln som daterat förtydligande i `businessReq.txt`. Bevara det redan godkända stödet för noll exemplar. `krav.txt` och repots instruktioner gäller för beslut utanför detta uppdrag.

## 2. Etapp 1 · Åtkomst och migrationsgrund

### Åtkomstgränser

| Yta | Krav |
|---|---|
| Partner | Autentisera befintlig bearer-credential och härled klientens partnermandat på servern. Skilj läsning, köpregistrering, exemplar- och dokumentkomplettering åt som behörigheter. Klienter för kassa och lager kan ha mandat inom samma partner. Åtkomst från en annan partner kräver uttrycklig delegering. Första leveransen behöver bara kassa och lager inom samma partner. |
| Köpare | Härled aktiv part från autentiserat konto och serverkontrollerat medlemskap. Kontrollera denna relation för alla listor, detaljer och dokument. Användarens skickade part-ID får endast vara ett val inom det mandatet. |
| Admin | Skydda samtliga administrativa operationer på servern, inklusive äldre routes. Ge matchningsgodkännande och credentialhantering uttryckligt mandat. Audit ska ange aktör, åtgärd, objekt, tid och korrelations-ID. |
| Demo | Stöd **Visa som** och syntetiska, uttryckligen godkända fixtures endast i ett explicit demoläge med isolerad data. Alla demoendpoints och profilväxling via dem ska vara avstängda i skyddat läge. Skyddade konto- och adminvägar ska neka åtkomst utan betrodd autentisering. |

Återanvänd en betrodd autentiseringslösning om repot har en sådan. Om den saknas, inför en verifierbar gräns för sessionsidentiteten, testa den och konfigurera verklig identitetsleverantör före produktionsöppning. Håll en enkel testadapter under explicit demoläge och konfigurera skyddat läge att neka konto/admin utan fungerande identitetskälla. Ett aliasuppslag skapar aldrig en session.

Skapa partnernycklar med engångsvisning. Lagra verifierare och nyckel-ID med scopes och spärrstatus. Rotation utfärdar ny credential och återkallelse spärrar den gamla. Listor visar endast metadata. Migrera gamla nycklar till verifierare där de kan verifieras likvärdigt och ta bort klartext först när ersättningen är verifierad. Opta in testnycklar separat. Logga metod, route-mall, status, varaktighet och korrelations-ID. Skriv aldrig råa credentials, köparreferenser eller kvittoinnehåll till loggar och outbox.

### Schema som byggs vidare

Använd nya framåtriktade migrationer. I granskad checkout finns `internal/migrate/0001_init.sql` till `0005_order_amounts.sql`, ordertransaktioner i `internal/store/store.go` och läsrutter i `internal/httpapi/server.go`.

| Befintligt eller nytt | Implementation |
|---|---|
| `party_profiles` | Använd befintliga `id` som kanoniska part-ID:n. `parties` behåller de partnerlokala identiteter som gamla köp refererar till. Bevara befintliga UUID:n. Konto och mandat lagras separat från part. |
| Verifierade alias | Spara typ, utfärdare eller namespace, normaliserad HMAC med nyckelversion, verifieringsstatus, giltighetsintervall och evidensreferens. Ändrade bindningar ska kunna granskas. En äldre ostrukturerad aliasrad är en ledtråd tills den verifierats. |
| `purchase_events` | Behåll gamla referenser. Lägg till nullable `resolved_buyer_party_id` som FK till kanonisk part samt en referens till länkningsbeslutet. Tillåt att den partnerlokala köparen saknas på nya köp när ingen referens har lämnats. Indexera sparad köpare och köptid. Spara tillstånd för att skilja saknat underlag från godkänd koppling. |
| Orderrad och `purchase_products` | Lägg till frivillig orderrad med unik `(purchase_event_id, line_ref)`, positiv kvantitet, modell/GTIN och valfria belopp. Lägg en nullable radkoppling på den befintliga köpexemplarlänken. Kontrollera att raden tillhör samma köp. Bevara unikheten köp + exemplar. Många exemplar kan kopplas till en rad. |
| `product_instances` | Lägg till nullable `owner_party_id` med FK till `party_profiles.id`, index för ägarlistan och referens till aktuell auktoritativ ägarhändelse. En nullable typad kanonisk instansnyckel får vara tom på olösta äldre poster. En instansnyckel måste vara unik bland poster som har en sådan. |
| Ägarhändelser och matchningsbeslut | Lägg till kanoniska händelser vid sidan av gamla, partnerlokala `ownership_events`. Spara exemplar, tidigare/ny part, källa och evidensreferens, affärstid, registreringstid, aktör och korrelation. Första tilldelningen är unik per exemplar. Ett matchningsbeslut hör till ett konkret köp. Synka stämpel och händelse i samma transaktion. |
| Dokument och resurser | Koppla kvitto till köp och garanti till exemplar med eventuell referens till köp och garantibevis. Dokumentmetadata innehåller filtyp, storlek, hash, privat objektnyckel och källa. DPP och manual är resurslänkar. Befintlig instansresurs återanvänds. |

Inventera gamla alias och instansnycklar i torrkörning. Ett separat idempotent migreringskommando får stämpla äldre poster enbart från uttryckligen godkänt underlag. Tvetydiga dubbletter och gamla automatiska ägarhändelser utan godkänt bevis lämnas väntande. En appstart gör ingen matchning. Testa både tom och uppgraderad databas. En profil som inaktiveras får inte kaskadradera köp eller ägarhistorik.

## 3. Etapp 2 · Matchning och konsistent ägarstämpel

### Identifiera köparen och exemplaret

- Behandla befintligt `buyer.external_id` som partnerlokalt som standard. Lägg till valfri `buyer.alias_type` för typad referens. Stöd `EMAIL`, `PHONE`, `NATIONAL_ID`, `ORGANIZATION_NUMBER`, `MEMBERSHIP_ID` och `PARTNER_CUSTOM_ID`. Utfärdarens scope bestäms genom autentiserad partner, konfiguration och betrott underlag. Ett värde i payload får inte välja en godtycklig utfärdare.
- Godkänn köpets köparkoppling automatiskt bara om exakt en verifierad bindning uppfyller en explicit policy för källan, köpet och dess tidpunkt. Annars spara som väntande. För skyddad drift där en sådan policy ännu saknas används ett separat adminbeslut med kontrollerad evidens. Godkännandet anger ett verifierat underlag som gäller det aktuella köpet.
- E-post och telefon kan använda tidsbegränsad engångsverifiering. Kontroll av dagens adress eller nummer bevisar kontroll vid den tidpunkten. Personnummer ska styrkas via betrodd e-legitimation eller motsvarande identitetskontroll. Medlemsnummer kräver bekräftelse inom rätt utfärdare. För organisationsreferenser krävs behörighet att företräda organisationen. Produktionsleverantör och slutlig evidenspolicy ligger efter denna leverans. [NIST om identitetsverifiering](https://pages.nist.gov/800-63-4/sp800-63a/proofing/)
- Tolka befintliga identifierarlistor till en godkänd instansnyckel. Första stödet är giltigt GTIN + serienummer, exempelvis `{"scheme":"serial","value":"DEMO-123","qualifiers":{"gtin":"09524000059109"}}`. Validera kontrollsiffra och normalisera GTIN till 14 siffror. Bevara serienumrets betydelse enligt schemat. Ett alternativt tillverkar-ID får lösa exemplar endast inom konfigurerad utfärdare. GTIN ensamt är rad- eller modellinformation. Godtyckliga gamla identifierare får sparas som väntande produktunderlag, med status och utan ett påhittat exemplar-ID. Motsägelse mellan giltiga identiteter ger konflikt.

### Genomför tilldelningen i befintligt skrivflöde

1. `CreateOrder`, `AddProducts` och uttryckligt godkännande anropar gemensam tilldelningslogik. Kontrollera autentisering och åtkomst före idempotent svar. Matcha redan accepterade äldre anrop med tidigare normalisering innan strängare regler för nya skrivningar tillämpas.
2. Lås köp och berörda exemplar i deterministisk ordning. Använd sparad godkänd köparkoppling om den finns. Saknas bindning eller fysisk identitet, spara väntande underlag med källa och referens till köp och eventuell orderrad. Om en äldre fysisk post har olöst ägarhistorik eller motstridiga dubbletter ska den ligga kvar för granskning. En tom ny ägarstämpel på en sådan post räcker inte som grund för tilldelning. Ett tidigare godkänt köp behåller sin köpare även om aliasregistret ändras före lagerkomplettering.
3. När både godkänd köpare och identifierat exemplar finns och policy för köpets tilldelning är uppfylld, tilldela bara om `owner_party_id IS NULL`. Verifiera resultatet efter samtidiga skrivningar. Spara händelse, stämpel, audit och outbox tillsammans.
4. Samma part på ett redan tilldelat exemplar får länka ytterligare tillåtet köpunderlag utan ny första ägarhändelse. Annan part ger `409 OWNERSHIP_CONFLICT`. Flera identifierare som pekar på olika exemplar ger `409 PRODUCT_IDENTITY_CONFLICT`. Rulla tillbaka hela det aktuella skrivkommandot, inklusive flerradsbatch. En redan accepterad order finns kvar. Inga ägaruppgifter i partnervända konfliktsvar.
5. Ett uttryckligt matchningsbeslut för ett redan godkänt köp är idempotent för samma beslut. Ett annat målpart-ID ger `409 BUYER_ALREADY_LINKED`. Ett framtida särskilt rättelseflöde får hantera verkliga fel. Ett vanligt partneranrop eller adminformuläret för profil får inte ändra en godkänd köparkoppling.

När flera deloperationer på samma order kan köras samtidigt ska databasen upprätthålla unik instansnyckel, en första tilldelning och högst känt radantal. Returnera stabila domänfel. Underlag som sparas som väntande får inte exponera en befintlig ägares resurser.

## 4. Etapp 3 · API och användarflöde

### Kontrakt

| Operation | Implementera |
|---|---|
| `POST /partner/v1/orders` | Behåll `202` och befintliga svarsfält. Stöd utebliven `buyer`, valfri `buyer.alias_type`, valfri `lines` och fortsatt stöd för `products: []`. Behåll `seller.external_id`, orderreferens, tid och befintliga orderbelopp för nya order. Gör `buyer.type` och `seller.type` valfria, med `UNKNOWN` som standard. |
| `POST /partner/v1/orders/{order_ref}/products` | Behåll `202`, `event_ref` och `products`. Stöd valfri `line_ref` per exemplar. Om `line_ref` anges måste den tillhöra köpet. Acceptera komplettering utan pris. När belopp rapporteras måste befintliga valideringar av total, moms och netto fortsätta gälla. När en rad har totalbelopp och moms räknas raden en gång mot ordern. Eventuella rapporterade belopp på radens exemplar kontrolleras mot raden. För rader utan belopp och exemplar utan rad räknas tillgängliga exemplarbelopp direkt mot ordern. Kontrollera summorna i samma transaktion vid skapande och komplettering. |
| `GET /partner/v1/orders/{order_ref}` | Svara `200` för order inom mandat. Returnera `order_ref`, `buyer_status`, `product_status`, `products`, `pending_product_count` och `documents`. Exponera endast egna orderkopplingar, dokumentmetadata och tillåtna exemplaruppgifter. |
| `POST /api/v1/admin/purchases/{purchase_id}/buyer-link` | Kräver matchningsmandat. Body har `decision_ref`, `party_id` och `evidence_ref`. Servern slår upp en tidigare registrerad evidenspost och verifierar att den är godkänd för exakt detta köp och denna part. Ett fritt textvärde får inte utgöra evidens. Första leveransen stödjer en auditloggad adminåtgärd för registrering av granskat underlag, med köp-ID, part-ID, metod, källa och vilken tid underlaget styrker. Spara endast nödvändig metadata och en referens till skyddat underlag. Samma beslut ger samma svar. Svara `200` med matchningsstatus och antal tilldelade exemplar. |
| `POST /partner/v1/orders/{order_ref}/documents` | Lägg till privat PDF-kvitto som multipart med fälten `file` och `document_ref` för retry. Svara `201` med dokument-ID och status när fil och association är klara. Samma dokumentreferens och fil återger samma svar. Byte av innehåll under samma referens ger `409`. Lämna ingen läsbar dokumentpost vid avbruten eller underkänd uppladdning. |
| `GET /api/v1/me/products` och `GET /api/v1/me/products/{product_id}` | Filtrera på aktiv parts sparade `owner_party_id`. Detalj visar exemplar, tillåten proveniens, garanti och resurser. |
| `GET /api/v1/me/purchases` och `GET /api/v1/me/purchases/{purchase_id}` | Filtrera på sparat `resolved_buyer_party_id`. Visa tidigare köp även efter överlåtelse, samt kvitto enligt köprättigheten. Produktdetaljer med ägarkrav kontrolleras separat. |
| `GET /api/v1/me/purchases/{purchase_id}/documents/{document_id}` | Kontrollera både köprättighet och dokumentets koppling. Strömma privat fil eller ge en kortlivad signerad länk efter godkänd åtkomst. |

**Orderstatus.** Tillåt noll rapporterade produkter i skrivsvaret när inget exemplar har kunnat identifieras. Använd `buyer_status` med värdena `AWAITING_REFERENCE`, `PENDING_MATCH` eller `LINKED`. Använd `product_status` med fyra värden. `AWAITING_PRODUCTS` betyder att inga exemplar har identifierats. `PARTIAL` betyder att det finns identifierade exemplar och kvarvarande väntande underlag eller återstående deklarerat antal. `REGISTERED` och `COMPLETE` förutsätter att det inte finns väntande produktunderlag. `REGISTERED` betyder att exemplar finns men slutantal saknas. `COMPLETE` kräver att deklarerade antal är uppfyllda för samtliga rader, utan väntande produktunderlag, eller ett uttryckligt slutbesked i ett framtida kontrakt. Tom order får aldrig automatiskt status `COMPLETE`. Ett väntande produktunderlag har ingen plats i listan över lösta exemplar.

**Fel och kompatibilitet.** Behåll befintlig semantik för `order_ref` och `event_ref` inom partners scope. Ändrat innehåll under samma referens ger `409 IDEMPOTENCY_CONFLICT`. Behåll ursprungligt svar för godkända retries och använd GET för levande status. Returnera `401` för saknad eller ogiltig autentisering, `403` för otillräcklig åtgärdsbehörighet och `404` för objekt utanför mandat. Dokumentera stabilt `code` i `application/problem+json`. Propagera `X-Correlation-ID`. Bevara exakta decimaler och dokumentera alla schemaändringar i OpenAPI 3.1 med körbara exempel.

### Referensflöde för API-konsol och acceptanstest

Kassans första begäran till `POST /partner/v1/orders`:

```json
{
  "order_ref": "DEMO-4711",
  "occurred_at": "2026-11-22T10:00:00Z",
  "buyer": {"external_id":"member-42","alias_type":"MEMBERSHIP_ID"},
  "seller": {"external_id":"testbutiken"},
  "currency": "SEK",
  "total_amount": 899.00,
  "vat_amount": 179.80,
  "lines": [{"line_ref":"1","quantity":1,"gtin":"09524000059109"}],
  "products": []
}
```

Lagret skickar till `POST /partner/v1/orders/DEMO-4711/products`:

```json
{
  "event_ref": "warehouse-4711-1",
  "products": [{
    "line_ref": "1",
    "identifiers": [{"scheme":"serial","value":"DEMO-123","qualifiers":{"gtin":"09524000059109"}}],
    "resources": [{"type":"DPP","uri":"https://example.com/dpp/demo-123","access_level":"OWNER","resolver_type":"DIRECT_HTTPS"}]
  }]
}
```

En syntetisk verifierad köpare ska nu se exemplaret. GET-status ska visa en löst produkt och `COMPLETE` eftersom den enda raden är uppfylld. Testa också `lines` utelämnat och förvänta `REGISTERED` efter samma komplettering utan radreferens.

### UI

- **Mina produkter** är startsida. Visa förvärvsdatum, produktnamn eller modell, serienummer, garanti när den finns och tillåtna DPP-länkar. Ingen extern resolution i ordertransaktionen. Första versionen visar registrerade länkar och ett tydligt tillstånd när länk saknas.
- **Mina köp** visar säljare, datum, referens, belopp, exemplarstatus och kvitto. Visa *Inväntar produktuppgifter* för köp utan identifierade exemplar. Formatera SEK med två decimaler i UI och behåll API:ts exakta precision.
- **Admin** visar och kan söka köp på referens och partner. Visa produktstatus, matchningsstatus, underlag och historik. Använd särskild åtgärd för godkännande. Profilredigering påverkar enbart profiluppgifter. Visa maskerad credentialmetadata.
- **API-konsol** är separat frontend-entrypoint i samma repo och använder OpenAPI. Visa testpartner, operation, pathfält, begäran och svar. Lägg **Skicka** ovanför begärans innehåll. Visa status, korrelations-ID och körbara exempel för order, komplettering, GET och retry.
- **Demo** visar uttryckligt **Visa som**. Rensa gamla data vid profilbyte och ignorera svar från tidigare profilval.

Knyt garantin till exemplar. Stöd en behörighetskontrollerad adminåtgärd för att registrera individgaranti. Spara start- och slutdatum när känt, plus villkorslänk eller garantibevis. Hitta inte på garantitid. Kvitto har köpbehörighet och ligger privat. Varje resurs filtreras enligt publik, aktuell ägare eller behörig roll. Implementera privat PDF-uppladdning med konfigurerad storleksgräns, innehållsvalidering och hash. Återanvänd befintlig lagringsadapter där sådan finns, annars använd S3-kompatibel privat lagring.

## 5. Etapp 4 · Verifiera leveransen

| ID | Acceptanstest |
|---|---|
| A01 | Köp utan buyer och utan rader eller exemplar accepteras. Köp med godkänd köpare och noll exemplar visas i Mina köp. |
| A02 | Registrera köp följt av sen lagerkomplettering. Kör omvänt med exemplar före godkänd matchning. Första ägarstämpeln uppstår exakt en gång när båda kraven är uppfyllda. |
| A03 | Godkänn ett köp, byt eller återanvänd alias innan lagerkomplettering. Exemplaret går till sparad köpare. Efter tilldelning har uppstart, inloggning och profilredigering ingen effekt på köpare eller ägare. |
| A04 | Samma medlemsnummer hos två utfärdare hålls isär. Tvetydiga kandidater och verifiering utan stöd för äldre köp lämnas väntande. |
| A05 | GTIN ensamt skapar ingen ägd instans. Två serienummer på samma GTIN får olika exemplar-ID. Tre exemplar kan komplettera en rad med kvantitet tre. Ett fjärde avvisas atomärt. |
| A06 | Två samtidiga tilldelningar till olika parter ger högst en ägare. En gammal order får aldrig skriva över ett redan tilldelat exemplar. Ett konfliktexemplar i batch rullar tillbaka hela kommandot. |
| A07 | Idempotent retry av befintliga anrop ger samma sparade svar. Ändrad payload ger konflikt. Tvingat fel före commit lämnar ingen delskriven stämpel, händelse eller outboxpost. |
| A08 | Fel partner, fel part och otillräcklig adminroll nekas. Kvitto är privat. Tidigare köpares köpunderlag ger ingen tillgång till ägarstyrd DPP efter ägarbyte i testdata. Rå partnernyckel syns bara vid utfärdande. Demo är avstängt i skyddat läge. |
| A09 | Migration körs på ny och äldre databas. Befintliga referenser, köp och historik bevaras. Äldre tvetydiga poster stämplas aldrig automatiskt. |
| A10 | Kör kassans och lagrets exempel, läs orderstatus och verifiera DPP i Mina produkter samt kvitto i Mina köp från webbläsaren. OpenAPI-exemplen matchar implementationen. |

Kör `go test ./...` med `TEST_DATABASE_URL` mot dedikerad PostgreSQL-testdatabas, `go vet ./...` samt `npm ci`, `npm run build` och `npm run lint` i frontend. Lägg API-kontraktsvalidering och frontendkontroller i CI. När ett gammalt test bygger på dynamisk aliasmatchning, oavgränsad demo eller automatisk ägaröverskrivning, ersätt just det förväntade beteendet med motsvarande acceptanstest. Behåll övrig täckning. Redovisa ändrade migrationsfiler, API-ändringar, resultat per acceptanstest och eventuella avvikelser.

## 6. Efter denna leverans

Planera separat implementation av produktionsverifiering, ägarbyte, retur, rättelse och avancerade DPP-resolvers. Ägarbytet ska kontrollera förväntad nuvarande ägare och mandat samt stänga föregående ägarperiod genom historiken. Bygg DPP-resolvers och signerade webhooks på outbox med separata tidsgränser, spårbar källa, kontrollerade omförsök och stabilt event-ID. Dokumentera retention, personuppgiftsansvar och insyn/rättelse före pilot med riktiga personuppgifter. Lägg till detta när respektive integrationsbehov och policy finns.

## Startkommando till agenten

> Läs THE_REUCE_IMPLEMENTATION.md från början till slut. Implementera etapp 1 till 4 i den aktuella checkouten. Börja med att verifiera de befintliga orderkontrakten och skriv regressionstesterna för sparad köparkoppling, låst ägarstämpel och samtidig tilldelning. Fortsätt sedan i körbara delsteg. Uppdatera businessReq.txt, migrationer, API, UI och tester där respektive etapp kräver det. Kör verifieringen och redovisa faktiska resultat och avvikelser.
