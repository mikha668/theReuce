# theReuce – implementationsspecifikation för GitHub Copilot

## Uppdrag

Implementera ett partnerdrivet köp- och ägarregister i befintligt Go-, PostgreSQL- och React-repo. En partner ska kunna registrera ett köp med noll eller flera exemplar. Lagret ska kunna komplettera med exemplar senare. Köparen ska kunna se sina aktuella produkter, sina historiska köp, kvitton, garanti och tillgängliga DPP-länkar.

**Bindande regel från beställaren:** När ett fysiskt exemplar har identifierats och matchningen till en ägare har godkänts ska ägarens stabila interna part-ID skrivas till `product_instances.owner_party_id`. Produkten är därefter bunden till den parten. Läsningar och uppstart ska använda redan sparade kopplingar. Ändring, borttagning, återverifiering eller återanvändning av ett alias får aldrig ändra vem som äger ett redan tilldelat exemplar. Framtida ändringar av ägandet får bara göras genom en separat behörighetskontrollerad domänoperation med revisionsspår.

**Instruktion till Copilot:** Läs `AGENTS.md` om den finns, `businessReq.txt`, `krav.txt`, `todo.txt`, `README.md`, OpenAPI och berörd kod innan du ändrar något. Lägg beställarens bindande regel som daterat kravförtydligande i `businessReq.txt` och behåll befintligt godkänt stöd för köp med noll produkter. Modell, API-namn och statusar nedan är föreslagna implementationsval. Stäm av avvikelser mot aktuell version av repot. Arbeta i etapper, uppdatera kontrakt och tester i samma ändring och rapportera exakt vad som verifierades. Följ repots krav på godkännande om du behöver ändra ett affärskrav utöver detta uppdrag.

Granskningsunderlaget var commit `eb4d71ef8d7adca6c813dccc6ae6e53b13d599d1`. Vid en nyare checkout gäller den aktuella kodens migrationsnummer och kontrakt. Behåll befintlig modulär monolit, transaktionell outbox och partner-API. Använd nya PostgreSQL-migrationer för uppgraderingar.

## Fynd att åtgärda

| Prioritet | Fynd i granskat repo | Åtgärd |
|---|---|---|
| P0 | `internal/httpapi/server.go`, `partyPurchases`, matchar profilens alias mot alla partner-ID:n vid läsning. En senare aliasändring kan ändra vilka historiska köp som visas. | Ersätt dynamisk matchning med sparad köpkoppling och sparat ägar-ID. Scope och verifieringsstatus ingår i matchningspolicyn. |
| P0 | `internal/store/store.go`, `addProduct`, lägger till ägarhändelser vid köpkomplettering. Samma redan ägda exemplar kan återkomma i en annan order. | Gör första ägartilldelningen till en transaktionell och villkorad skrivning. Ett vanligt order- eller kompletteringsanrop får aldrig byta ägare. |
| P0 | `product_identifiers` kan matcha globalt på godtyckligt schema, värde och kvalificerare. En GTIN eller ett lokalt serienummer räcker i dagens kod. | Inför en verifierad nyckel för exemplar, med unikhet i databasen. Bevara övriga identifierare som metadata eller avgränsade alternativnycklar. |
| P0 | Admin och köpar-API saknar autentisering. Partnerlistan returnerar råa nycklar. Produktresurser väljs utan policy för ägar- och rollstyrd åtkomst. | Skydda ytorna separat. Använd objektspecifik behörighet och verifierare för partnernycklar. Kör profilväljare och testnycklar i demo. |
| P1 | Köparvyn visar endast historiska köp. Partnern har inget status-GET. Sen produktkomplettering kräver pris och moms på nya order. | Lägg till egna läsflöden för produkter, köp och status. Gör lageruppgifterna oberoende av prisdata när kassan redan har dem. |
| P1 | Resurslänkar lagras men visas inte. Kvitto och garanti är idag resurstyper på en produkt. Konsolen är en flik med en egen handskriven kontraktskälla. | Knyt kvittot till köpet. Ge garanti och DPP separata funktioner på produktsidan. Bygg den separata API-konsolen från OpenAPI. |

## Datamodell och invarianta regler

| Del | Implementation |
|---|---|
| **Kanonisk part** | Använd ID:t på den oberoende parten som ägar-ID. I den granskade modellen kan `party_profiles.id` vara första kanoniska ID, medan gamla `parties` representerar partnerlokala inrapporterade identiteter. Bevara befintliga profil-ID:n vid ett senare namnbyte. Ett produktägarskap pekar aldrig på en API-klient, ett alias eller ett konto-ID. |
| **Konto och mandat** | Separera inloggningsidentitet från part. Ett konto får agera för en part genom verifierat medlemskap eller uttrycklig behörighet. Köpare, säljare och ägare är roller i respektive affärsrelation. |
| **Alias och verifiering** | Lagra alias som typ, utfärdare eller namespace, normaliserad matchningsnyckel, verifieringsstatus, verifieringsmetod samt giltighetstid. Använd typ- och utfärdarspecifik normalisering och matchning. Bevara historik över återkallelse och ändrad bindning. Saknad verifiering eller flera tillåtna kandidater lämnar ärendet olöst. |
| **Köp och köparlänk** | Bevara rapporterad köparreferens eller dess skyddade matchningsunderlag. Lägg till en separat kanonisk köparkoppling, exempelvis `resolved_buyer_party_id`, med FK, beslutstid, metod och beslutsreferens. Behåll befintliga partnerlokala köparfält för källhistorik. Ett köp med godkänd köparlänk kan visas även utan registrerat exemplar. |
| **Instans och ägarstämpel** | Lägg till nullable `product_instances.owner_party_id` med FK till den kanoniska parten och index för ägarlistor. Spara också tilldelningstid och referens till ägarhändelsen. `NULL` betyder väntande tilldelning. Undvik generiska uppdateringsvägar till ägarfältet och indexera den kanoniska instansnyckeln med unikhetsvillkor. |
| **Ägarhändelser** | Spara en auktoritativ append-only-historik med exemplar, tidigare och ny part, typ, källa, affärstid, registreringstid, aktör och korrelations-ID. Befintliga ägarhändelser från tidigare kod är underlag tills de validerats. Vid behov lägg en separat ny tabell för kanoniska händelser, så att äldre partnerlokala ägar-ID:n kan bevaras spårbart. |
| **Dokument** | Kvitto kopplas till köp. Individuellt garantibevis kan kopplas till exemplar och köp. Modellera åtkomst separat från DPP- och manuallänkar. Lagra filer privat och metadata med källa, status, filstorlek och innehållstyp. |

För GS1-spåret ska **GTIN + serienummer** identifiera ett exemplar. Validera GTIN och normalisera det till gemensam representation. Bevara serienumrets betydelse enligt utfärdarens regler. För andra instans-ID:n krävs ett uttryckligen betrott schema och utfärdarens namespace. När underlaget bara har GTIN, spara vid behov en orderrad eller ett väntande produktunderlag. Skapa ingen ägarstämpel för den uppgiften förrän det fysiska exemplaret kan identifieras. [GS1 Digital Link – snabbguide](https://ref.gs1.org/docs/2024/digital-link-quick-start-guide)

## När ägaren skrivs

1. **Registrera köp.** Autentisera klienten och kontrollera handlarmandatet. Spara köpunderlaget även om köparlänk, exemplar eller båda saknas. Om köparmatchningen redan uppfyller policyn, spara det kanoniska köpar-ID:t på köpet.
2. **Komplettera exemplar.** Hitta köpet genom partnerns mandat och orderreferens. Identifiera exemplaret med dess kanoniska instansnyckel och länka det till rätt köp eller orderrad. Använd köpets redan sparade köparlänk om sådan finns. Ett senare byte av profilalias ska aldrig påverka denna operation.
3. **Slutför en väntande köparmatchning.** Efter en verifieringshändelse eller uttryckligt granskningsbeslut ska en skrivoperation länka det konkreta köpet och försöka tilldela dess väntande exemplar. För äldre köp eller återanvänt alias ska det finnas bevis som täcker det köp och den tidpunkt beslutet gäller. En verifiering av dagens e-postadress eller telefonnummer räcker inte ensam för att låsa upp äldre köp.
4. **Tilldela atomärt.** I samma PostgreSQL-transaktion som den godkända tilldelningen, lås produktraden eller använd en villkorad skrivning som endast tilldelar om `owner_party_id IS NULL`. Kontrollera status efter samtidiga skrivningar. Spara ägarhändelse, ägarstämpel, audit och outbox tillsammans. Lås flera exemplar i stabil ordning. En retry av samma godkända tilldelning ska återanvända resultatet utan ny ägarhändelse.
5. **Hantera redan stämplat exemplar.** Samma ägare kan förekomma på nytt underlag utan att en ny första tilldelning skapas. Om en annan part anges ska ordinarie köpintag lämna stämpeln och historiken orörda. Registrera ett avvikelseärende eller returnera ett överenskommet `409` för kompletteringen. Dela aldrig uppgifter om nuvarande ägare i partnerns konfliktsvar. Om en order samtidigt sparas trots en olöst produktkonflikt ska det uttryckligen framgå i svaret och orderstatusen. Undvik tyst delvis framgång.
6. **Läs direkt.** Produkter hämtas via `owner_party_id`, och köp via sparad köparkoppling. Varken inloggning, profilladdning, uppstart, vanlig redigering av alias eller generell bakgrundssynk får tilldela om ägare. Ett explicit rättelse-, retur- eller överlåtelseflöde kan införas separat. Det ska kräva rätt mandat och förväntad aktuell ägare, samt journalföra ändringen atomärt.

**Implementationsregel för första versionen:** Fokusera på första tilldelningen och skyddet för redan stämplade exemplar. Lämna befintligt ägande orört i konflikter. Säkerställ att en senare separat livscykeloperation kan byggas utan att öppna ägarfältet för vanlig orderuppdatering.

### Verifiering i demo och produktion

| Alias | Rekommenderat verifieringsunderlag |
|---|---|
| E-post | Tidsbegränsad engångsverifiering som visar kontroll över brevlådan vid verifieringstillfället. |
| Telefon | Tidsbegränsad engångsverifiering som visar kontroll över numret vid verifieringstillfället. Återanvändning och äldre köp hanteras enligt länkningspolicyn. |
| Personnummer | En verifierad uppgift från en betrodd e-legitimationstjänst, exempelvis genom en framtida BankID-integration, eller motsvarande beslutad identitetskontroll. Att ange ett personnummer styrker inte identiteten. |
| Organisations- eller medlemsnummer | Verifierad koppling till organisationens mandat eller verifiering från den utfärdande organisationen inom rätt namespace. |

Demo kan använda syntetiska alias som har lagts in som förverifierade eller godkänts genom en uttrycklig demoåtgärd. Modellera verifiering och uppslag så att detta går att byta mot ett produktionsflöde. Aktivera demo explicit. Tillåt aldrig demoverifiering eller profilväxling via produktionsrutter. Lägg känsliga värden utanför vanliga listor, loggar och outbox-payloads. Använd vid behov en versionssatt HMAC-baserad matchningsnyckel och lagra råvärde separat endast om verksamhetsflödet behöver det. [NIST SP 800-63A-4 om identitetsverifiering](https://pages.nist.gov/800-63-4/sp800-63a.html)

## API och behörighet

| Yta | Förväntat beteende |
|---|---|
| `POST /partner/v1/orders` | Behåll partneravgränsad `order_ref` och idempotens. Tillåt noll produkter. Modellera typad köparreferens och valfri dokumentreferens. Behåll kompatibilitet för gamla payloads genom uttrycklig versions- eller övergångsregel. Gör buyer/seller `type` valfritt när det gamla kontraktet migreras och bevara gamla svar för retries. |
| `POST /partner/v1/orders/{order_ref}/products` | Behåll `event_ref` och idempotens. Stöd valfri `line_ref`. Tillåt lagerkomplettering utan upprepade prisfält när orderraden redan har pris eller pris är okänt. Validera belopp där de faktiskt rapporteras, utan att härleda noll från avsaknad. |
| `GET /partner/v1/orders/{order_ref}` | Returnera aktuell orderstatus inom handlarens mandat, samt registrerade exemplar och dokumentstatus. Redovisa komplett först när förväntat antal eller slutbesked finns. Undvik att exponera kanoniskt ägar-ID, personnummer eller andra parts uppgifter. |
| `GET /api/v1/me/products` och produktsida | Härled aktiv part från autentiserat konto och medlemskap. Filtrera på stämplad ägare. Kontrollera resursåtkomst vid varje hämtning och returnera endast tillåtna DPP-, garanti- och dokumentuppgifter. |
| `GET /api/v1/me/purchases` och köpdetalj | Filtrera på sparad köparkoppling. Visa även köp utan exemplar, säljare och datum samt tillåtna kvitton. Historiska köp och aktuellt ägande läses oberoende av varandra. |
| Dokument | Använd privat filuppladdning med storleks- och typkontroll och efterföljande koppling till köp. Dokument-ID kan föras med redan vid köptillfället. För hämtning, kontrollera köpet eller resursens rättighet först. |
| Admin | Skydda partner-, part-, köp- och ärendehantering. Rollen för matchningsbeslut ska vara explicit. Visa maskerat credential-ID i listor. Visa partnerhemlighet en gång vid utfärdande, lagra verifierare och stöd rotation och återkallelse. |

Partnerbehörighet ska härledas från autentiserad klient och explicit handlarmandat. Företagskonton och partners får åtkomst inom sin roll och sitt scope, även när samma exemplar förekommer i flera affärshändelser. Ta bort eller avgränsa äldre öppna admin-, part- och login-rutter. Avveckla aliasuppslag som ger profilåtkomst. Upprätthåll objektspecifik auktorisering på servern, även för UUID:n och dokument-ID:n. Redovisa konkreta verksamhetsfel utan att logga personidentifierare, nycklar eller kvittoinnehåll. [OWASP om objektbehörighet i API](https://api-security.owasp.org/editions/2023/en/0x11-t10/)

I OpenAPI ska ett ursprungligt idempotent skrivsvar och aktuell GET-status vara två tydliga kontrakt. Bevara befintliga idempotensreferenser och normaliseringsregler för redan accepterade anrop. Om de ändras, versionssätt kanoniseringen och behåll verifiering mot tidigare sparat underlag. Samla valideringsexempel och stabila felkoder i specifikationen.

## UI som ska levereras

- **Mina produkter** är startvyn. Visa aktuell produktlista med produktnamn, identifierande serienummer, förvärvsdatum och status för DPP och garanti. Produktsidan länkar till köpunderlaget när användaren har rätt att se det. Visa också ett tydligt tillstånd när extern information saknas eller är otillgänglig.
- **Mina köp** visar säljare, datum, orderreferens, belopp, kvitto och kopplade exemplar. Visa **Inväntar produktuppgifter** på ett köp utan exemplar. Formatera SEK-belopp med två decimaler i UI. Ändring av API:ts antal tillåtna decimaler är ett separat kontraktsbeslut.
- **Admin** får sektioner för köp, parter, partners och matchningsärenden. Visa källpartner, status, godkänt länkningsbeslut och historik. Visa typ och utfärdare per alias. Ett klick på **Spara profil** får aldrig tilldela eller flytta ett exemplar.
- **Demo** får ett uttryckligt **Visa som** med syntetiska profiler. Rensa föregående profils data direkt vid byte och ignorera svar för en profil som inte längre är vald.
- **API-konsolen** blir en separat app eller ett separat sandbox-entrypoint. Bygg operationer och exempel från OpenAPI. Placera **Skicka** ovanför requestfälten. Visa HTTP-status, korrelations-ID och svar. Använd metod + sökväg som operationens identitet. Testpartner kan väljas automatiskt i en isolerad sandlåda. Produktionsnycklar får aldrig hämtas till konsolen.

## Migrering och leveransordning

1. **Skydda driftlägena.** Avgränsa demo och admin, ta bort utlämning av partnerhemligheter i listor och lägg mandatkontroller på skyddade endpoints. Dokumentera startkonfiguration för demo och skyddad miljö.
2. **Inför stabil identitet och ägarstämpel.** Lägg till nya schemafält, ägarhändelser och skyddad skrivväg. Hantera koppling från `party_profiles` eller motsvarande stabil kanonisk part. Koppla nya order- och kompletteringsflöden. Ersätt aliasbaserade läsningar och lägg till de nya testerna innan UI växlas över.
3. **Hantera äldre data genom explicit migrering.** Inventera kända kopplingar, dubbletter och konflikter i en torrkörning. Äldre ägarhändelser kan vara underlag men deras skrivordning är inte verifierat ägande. Bara beständigt och godkänt underlag får ge automatisk stämpel via en versionssatt migrering. Lämna övriga poster väntande för granskning. Migreringen ska vara idempotent och testas både på tom och uppgraderad databas. Start av appen ska aldrig utföra denna matchning.
4. **Leverera ett sammanhängande UI- och partnerflöde.** Lägg till GET-status, orderdokument, produktsida och DPP-länk, köpvy och adminöversikt. Separera konsolen. Lägg till OpenAPI-validering, frontendens bygg/lint och flödestester i CI.
5. **Planera nästa steg efter grundflödet.** Integrera vald verifieringsleverantör och separat överlåtelse-, retur- eller rättelseflöde före en pilot som kräver dem. Aktivera resolverarbete och webhookleverans utifrån pilotens behov. Behåll externa DPP-anrop utanför ordertransaktionen.

## Acceptanstester för Copilot

| Test | Godkänt resultat |
|---|---|
| Köp utan produkt och verifierad köpare | Köpet visas direkt för rätt part. Sen komplettering stämplar exemplaret med det redan sparade part-ID:t. |
| Produkt finns före godkänd köparmatchning | Produkten kan länkas till köpet som väntande. En godkänd skrivoperation stämplar den exakt en gång. Läsning, uppstart och profilredigering lämnar den väntande. |
| Alias ändras, tas bort, verifieras igen eller återanvänds | Exemplar och tidigare godkända köpkopplingar behåller sina sparade part-ID:n. En ny aliasinnehavare får ingen historik automatiskt. |
| Samma medlemsnummer hos två utfärdare | Köp och produkter kopplas bara inom rätt namespace. Två likadana kandidater förblir olösta. |
| Samma exemplar anmäls för en annan part | Stämpel och befintlig historik består. Ett kontrollerat fel eller ärende går att följa utan att ägardetaljer exponeras. Testa även en gammal order som kompletteras efter en senare ägarhändelse. |
| Två samtidiga första tilldelningar och API-retry | Högst en första ägare och en tilldelningshändelse. Samma kommando kan köras om. Databasfel rullar tillbaka stämpel, historik och outbox tillsammans. |
| Två exemplar med samma GTIN och olika serienummer | Två olika interna exemplar-ID:n. Samma kanoniska instansnyckel kan inte skapa två fysiska poster. |
| Åtkomstkontroll | Användare ser enbart tillåtna köp, produkter och dokument. Partnerns orderstatus begränsas av mandat. Partnerlistor innehåller aldrig den råa nyckeln. Demoåtkomst stängs av i skyddat läge. |
| Ägarstyrd resurs och historiskt kvitto | Köparen kan öppna sitt historiska kvitto med köpbehörighet. En ägarstyrd produktresurs kräver aktuell ägarbehörighet. |
| Uppgradering | Befintliga orderreferenser och retries fungerar. Tvetydiga äldre ägarunderlag auto-stämplas inte. Test som tidigare förväntade sig att en aliasändring flyttade synlighet ersätts med det uttryckligen godkända stabila beteendet. |

Kör `go test ./...` med `TEST_DATABASE_URL` mot en isolerad PostgreSQL-testdatabas, `go vet ./...`, `npm ci`, `npm run build` och `npm run lint`. Kör de nya integrationstesterna på tomt schema och en representativ uppgradering. Genomför minst ett webbläsarflöde från partneranrop och lagerkomplettering till konto, kvitto och DPP-länk. Rapportera testresultat, datamigreringens utfall, kvarstående blockerare och varje kontraktsändring. Granskningsversionen passerade Go-testerna med PostgreSQL och frontendens bygg/lint. De nya funktionerna och acceptanstesterna ovan är beställd implementation.

## Börja i dessa filer

- `businessReq.txt` och `krav.txt` för kraven och `todo.txt` för UI-önskemål.
- `internal/migrate/` för schema och uppgradering, `internal/store/store.go` för order och tilldelning, `internal/orders/` för input och belopp.
- `internal/httpapi/server.go` för partner-, konto- och adminrutter. Lägg domänbeslut i applikations- eller lagringslagret där transaktionsgränsen kan upprätthållas.
- `openapi/openapi.yaml`, `docs/partner-api.md`, `docs/database-model.md` och `README.md` för kontrakt och driftinstruktioner.
- `frontend/src/App.tsx` och `frontend/src/App.css` för nuvarande UI. Dela upp efter funktion när de nya vyerna införs.

### Startprompt för Copilot Chat i VS Code

> Läs theReuce-implementationsspecifikation.md och repots instruktioner. Börja med etapp 1 och 2. Genomför den persistenta ägarstämplingen i PostgreSQL och använd sparade kopplingar för konto- och köpläsning. Behåll partnerns skrivflöde och köp med noll exemplar. Skriv först ett integrationstest som visar att en aliasändring aldrig ändrar ägare och ett som visar att ett redan stämplat exemplar inte får ny ägare genom orderintag. Implementera migrationsändringen och relevanta behörighetskontroller, kör de befintliga och nya testerna och redovisa ändringarna med filreferenser och vilka verifieringar som passerade. Fortsätt enligt etapperna när grundflödet fungerar.
