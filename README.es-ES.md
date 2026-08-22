*Léelo en [English](README.md).*

> ## Advertencia de seguridad, léela antes de ejecutar nada
>
> **CloudAC vincula ambos canales de red a loopback de forma predeterminada y se
> niega a arrancar de otro modo salvo que configures un secreto compartido.** Ese
> valor por defecto es deliberado. Las versiones anteriores escuchaban en todas
> las interfaces sin autenticación alguna, lo que significaba que cualquiera que
> pudiera alcanzar uno de los dos puertos podía expulsar a cualquier jugador o
> falsificar datos de detección.
>
> | Puerto | Escucha | Qué podría hacer un par no autenticado |
> |---|---|---|
> | **1234** | ComputationServer | Falsificar datos de movimiento y de habilidades de cualquier jugador, desactivar la detección para sí mismo, o lograr que se expulse a un jugador inocente |
> | **1212** | ACTransmitter, en tu servidor de Minecraft | Expulsar a cualquier jugador. Los UUID de Minecraft son públicos, así que no hace falta acceso previo |
>
> Si ejecutas los dos componentes en máquinas distintas, define `shared-secret`
> en `config.yml` y `CLOUDAC_SHARED_SECRET` en el demonio. Ambos quedan
> autenticados con HMAC-SHA256 y los mensajes llevan una marca de tiempo, de modo
> que una línea capturada no puede reproducirse. **Restringe ambos puertos, o no
> has restringido ninguno:** el demonio envía sus veredictos desde su propia
> máquina, así que filtrar solo el 1212 permite que quien alcance el 1234 haga
> que el demonio entregue una expulsión desde una dirección en la que tu firewall
> ya confía.
>
> **TLS no sustituye al secreto compartido.** TLS sin certificados de cliente
> autentica el *servidor* ante el *cliente*, y aquí ambos escuchadores son el
> servidor, de modo que un atacante simplemente habla TLS y envía la misma línea
> cifrada. El control que falta es la autenticación, no el cifrado.
>
> **Privacidad.** Ambos canales transportan UUID de jugadores y un flujo de
> posición a 20 Hz. Un UUID de Minecraft se resuelve a un nombre de usuario
> mediante un endpoint público, así que en el Reino Unido y la UE esto es dato
> personal, y la base de jugadores de Minecraft tiende a ser menor de edad.
> Activa el secreto compartido antes de que esto cruce una red, y considera una
> evaluación de impacto como punto de partida.
>
> Esto es una prueba de concepto. Lee "Limitaciones actuales" más abajo antes de
> ejecutarlo en un servidor que te importe.

# ¿Qué es CloudAC?

CloudAC es una prueba de concepto de un anti-cheat de Minecraft que ejecuta sus
comprobaciones y su procesamiento de datos en un programa aparte, para que el
lado de detección pueda crecer, con más comprobaciones, procesamiento más
pesado, otro lenguaje o más memoria, sin que nada de ese crecimiento recaiga
sobre el presupuesto de tick del servidor de Minecraft. El trabajo del servidor
de juego se reduce a enviar los campos relevantes de los paquetes y a actuar
sobre los veredictos que regresan.

### Por qué

Cualquier anti-cheat, por bien optimizado que esté, cuesta bastante más que un
plugin corriente. En los casos graves mejora la experiencia del jugador
baneando tramposos y la degrada por el lag que provoca. Mover la mitad cara
fuera de la máquina es una técnica real que usan varias redes grandes, y merece
ser más conocida.

### Detalles técnicos

CloudAC tiene dos partes.

**ACTransmitter** es un plugin de Spigot. Usa ProtocolLib para interceptar los
siete tipos de paquete que necesitan las comprobaciones, serializa los campos
relevantes en una línea delimitada por barras verticales y la entrega a una cola
acotada. Un hilo dedicado vacía esa cola sobre una única conexión persistente.
El plugin también posee el sistema de castigos, que actúa sobre los veredictos
que llegan por el canal de vuelta.

**ComputationServer** es un demonio independiente en Go. Mantiene un perfil por
jugador conectado, lo actualiza con cada línea entrante, ejecuta las
comprobaciones contra él y devuelve un veredicto cuando alguna se dispara.

El formato de cable es un marco por mensaje terminado en salto de línea,
`v1|<hmac>|<payload>`, con el payload delimitado por barras. El campo 0 del
payload es un dígito de tipo de paquete, el campo 1 el UUID del jugador y el
campo 2 una marca de tiempo que la comprobación de velocidad consume para
derivar un delta temporal real. El transmisor marca al ComputationServer en el
**TCP 1234**. El ComputationServer responde marcando al **TCP 1212**.

El escuchador de paquetes envía tanto el estado de suelo que **afirma el
cliente** como la **vista autoritativa del servidor**. Ese emparejamiento
importa, porque antes toda decisión de seguridad descansaba únicamente sobre la
afirmación del cliente.

#### Detalles técnicos, más opciones

Otra ventaja de tener un programa separado es que puede escribirse en el
lenguaje que convenga. Aquí se eligió Go porque es compilado, moderno, tiene
interfaces de modo que un diseño polimórfico de comprobaciones sigue
funcionando, y ya había familiaridad previa con él. Rust sería una opción
igual de buena.

### ¿Qué pasa con la latencia?

Hay dos latencias distintas en un diseño así, y mantenerlas separadas es todo el
argumento.

**La latencia de ida y vuelta del veredicto** es el tiempo desde que llega un
paquete sospechoso hasta que aterriza un castigo. CloudAC puede permitirse que
sea lenta, y esa es la ventaja genuina de un diseño silencioso. Hay dos maneras
de configurar un anti-cheat según tu prioridad: banear al tramposo, o prevenir
el cheat. Si tu prioridad es banear, no conviene aplicar un "setback"
(teletransportarlo a su última posición válida) en cuanto detectas algo, porque
un setback le avisa de que fue detectado y se adapta. Es mejor mantenerlo a
oscuras, reunir pruebas y actuar una sola vez. Un anti-cheat que tolera un
veredicto lento queda libre para mover sus comprobaciones fuera del servidor de
juego. Esta mitad del argumento se sostiene.

**La latencia de bloqueo inyectada en la tubería de paquetes** es otra cosa, y
es la que decide si el diseño funciona. Es tiempo que el servidor de Minecraft
pasa *esperando*, en el hilo que procesa los paquetes de un jugador, antes de
poder continuar con el tick. Afecta a todos los jugadores y no solo a los
tramposos, y ningún grado de silencio en el diseño de castigos lo abarata.

Esa distinción no es académica aquí. Una versión anterior de este proyecto abría
una conexión TCP nueva por cada paquete, en línea sobre el hilo de canal de
Netty. Medido contra la implementación actual en la misma máquina:

| Transporte | ns por paquete | bytes por paquete | sockets |
|---|---|---|---|
| Una conexión por paquete | ~500.000 | ~52.000 | 1 filtrado |
| Conexión persistente, flush por mensaje | ~15.000 | ~40 | 1 reutilizado |
| **Encolado acotado, vaciado fuera del hilo** | **~500** | **0** | **0** |

Ejecuta `make bench` para reproducir esa tabla. La primera fila varía en un
factor de dos aproximadamente entre ejecuciones, porque la dominan el
establecimiento de la conexión y el planificador del sistema. La última no,
porque nunca toca la red.

La ruta caliente es ahora un encolado. No puede bloquear, no puede asignar
memoria de forma significativa y no puede lanzar excepciones, así que las
condiciones de red ya no pueden afectar al juego, que es justo la propiedad que
este diseño afirma y que en versiones anteriores no tenía.

Una nota honesta sobre lo que eso consigue. Elimina un coste que introdujo el
transporte. No demuestra por sí solo que distribuir las comprobaciones aportara
algo, porque dos comprobaciones tan pequeñas siempre fueron lo bastante baratas
como para ejecutarse en el propio proceso. El argumento real a favor de la
separación es el margen: el lado de detección puede crecer sin que lo pague el
servidor de juego. Ese argumento es sólido. Simplemente no queda demostrado con
dos comprobaciones que no habrían costado nada de ninguna manera.

### Cómo ejecutarlo

```sh
make build          # ambos componentes
make test           # suite de tests de Go
make race           # tests bajo el detector de carreras
make fuzz           # fuzzing del parser del formato de cable
make bench          # mide la ruta caliente del transmisor
make check          # gofmt, go vet, govulncheck
```

Copia `ACTransmitter-<version>.jar` en `plugins/`, arranca el servidor una vez
para generar `plugins/ACTransmitter/config.yml`, y luego ejecuta el demonio:

```sh
CLOUDAC_LISTEN_ADDR=127.0.0.1:1234 \
CLOUDAC_TRANSMITTER_ADDR=127.0.0.1:1212 \
./bin/cserver
```

Entre máquinas distintas, define `CLOUDAC_SHARED_SECRET` y el `shared-secret`
correspondiente en `config.yml`. Genera uno con `openssl rand -base64 32`.

Ambos lados registran un latido cada 30 segundos con recuentos de paquetes,
perfiles seguidos y estado de conexión. **Un recuento de paquetes estancado
mientras hay jugadores conectados es la condición de alerta**, porque es la
única señal que distingue "ahora mismo no hay tramposos" de "la detección lleva
muerta desde el martes".

`/actransmitter status` informa de lo mismo desde el juego. `/actransmitter
punish off` detiene las expulsiones mientras la detección continúa, que es la
palanca que quieres durante un incidente de falsos positivos.

### Limitaciones actuales

Una lista honesta de lo que esto hace y no hace. Un prototipo debería decirlo en
voz alta en lugar de dejar que se descubra.

- **No uses `/reload` con este plugin.** Usa un reinicio completo. Un reload
  puede dejar la instancia anterior reteniendo el puerto de veredictos, y el
  síntoma es que los veredictos dejan de llegar en silencio.
- **Las constantes de la comprobación de velocidad son una primera
  aproximación y no están validadas contra datos reales de jugadores.** Se
  eligieron para separar el movimiento aéreo legítimo más rápido que se midió
  (0,40 bloques por tick) de un cheat de velocidad evidente, y la suite de tests
  fija ambos extremos. Necesitan ajuste contra trazas grabadas antes de que
  nadie dependa de ellas.
- **El movimiento con elytra y con riptide supera legítimamente los límites de
  la comprobación de velocidad** y actualmente no se excluye.
- **`CheckAbilities` solo detecta a un cliente lo bastante honesto como para
  anunciar una capacidad que no se le concedió.** Un cliente tramposo que vuela
  mediante paquetes de movimiento y omite el paquete de habilidades no la
  dispara. Detectar la ausencia de una mentira requiere otra comprobación.
- **El modelo de fricción detecta aceleración, no velocidad sostenida.** El
  movimiento a velocidad constante muy alta se detecta, el moderadamente alto
  no.
- **La procedencia del jar de ProtocolLib incluido no puede verificarse.** Su
  manifiesto dice `4.8.0-SNAPSHOT-b540` y toda la línea 4.x fue eliminada del
  repositorio original, así que no queda copia con la que comparar.
  `CHECKSUMS.txt` da integridad hacia adelante pero no puede dar autenticidad
  hacia atrás. Sustituirlo por tu propio ProtocolLib es la mejor opción.
- **Apunta a Minecraft 1.16.5.** Los índices de campo de paquete en
  `WrapperPlayClientFlying` son posiciones ordinales en el orden de campos de
  Mojang y devolverán valores incorrectos en silencio en otra versión.
  Vuelve a derivarlos y verificarlos antes de portar.
- **El ComputationServer mantiene el estado solo en memoria.** Un reinicio es
  seguro porque el plugin vuelve a anunciar a todos los jugadores conectados al
  reconectar, pero no hay persistencia ni una segunda instancia.

### Contribuir

CloudAC es un prototipo pensado para mostrar cómo podría funcionar el diseño de
un sistema así. Las contribuciones que lo amplíen y mejoren son bienvenidas.
ToDo actual, aproximadamente en el orden en que debería trabajarse:

1. **Validar las constantes de la comprobación de velocidad contra trazas de
   movimiento grabadas.** Es el trabajo de mayor valor disponible. La suite de
   tests ya tiene la forma, solo le faltan datos reales.
2. **Añadir la comparación con el estado autoritativo del servidor como una
   comprobación propia.** El formato de cable ya transporta tanto el estado de
   suelo que afirma el cliente como el del servidor. Un desacuerdo sostenido es
   una señal más fuerte que cualquiera de los dos valores por separado, y es lo
   que detecta directamente el módulo de trampas más común.
3. **Migrar a ProtocolLib 5.x** para poder eliminar el jar incluido y que la
   dependencia vuelva a ser visible para las herramientas. Ten en cuenta que
   5.3.0 es la última versión compatible con Java 8, así que esto no obliga por
   sí solo a actualizar el JDK.
4. **Mejorar el procesamiento de datos para que sea dinámico y no necesite
   comprobar el tipo de paquete.** Una advertencia: "dinámico" no debe
   significar "enviar todos los campos de todos los paquetes". La lista estrecha
   de registro es lo que mantiene el coste de tick del plugin cerca de cero.
5. **Excluir los estados de elytra y riptide de la comprobación de velocidad.**
6. **Añadir más comprobaciones.** Sigue la forma existente: una comprobación
   devuelve un veredicto, no abre un socket, y lee campos autoritativos del
   servidor siempre que una decisión de seguridad dependa de ellos.
7. Tus sugerencias e ideas.

### Aviso

No inventé la idea de ejecutar el cerebro de un anti-cheat aparte del servidor
de Minecraft. Esa idea existe desde hace tiempo y la usan algunos servidores
grandes. El objetivo de CloudAC es darla a conocer más ampliamente y, con
suerte, inspirar proyectos futuros que la adopten.

#### Aviso, estado actual del código

El código original se escribió con bastante rapidez y fue armado
precipitadamente hasta cierto punto, ya que publicarlo no estaba en los planes.
Desde entonces ha sido revisado en profundidad y reescrito en gran parte. La
sección "Limitaciones actuales" de arriba es la lista honesta de lo que queda,
y los tests cubren las partes que antes fallaban en silencio.

<br/><br/>
<br/><br/>

![resized](https://user-images.githubusercontent.com/24839815/174480405-35d2422c-f1b8-4035-a7c2-ff34a2cfb89a.png)
