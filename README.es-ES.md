# Qué es CloudAC
CloudAC es una prueba de concepto, un sistema anti-cheat micro para Minecraft que realiza todas sus comprobaciones y procesamiento de datos en un programa separado que puede instalarse en una máquina diferente para eliminar verdaderamente todo el impacto de rendimiento de las comprobaciones y los procesadores de datos (que son las partes más pesadas de un anti-cheat) del propio servidor de Minecraft; las únicas acciones relacionadas con el anti-cheat que realmente realiza el servidor de Minecraft es enviar todos los datos de paquetes relevantes al servidor de cómputo y el sistema de castigos.

### Por qué
CloudAC muestra una idea que funcionaría para resolver completamente uno de los mayores problemas de los anti-cheats de Minecraft basados en el servidor: esté de acuerdo o no, cualquier anti-cheat, incluso con las mejores optimizaciones y el código fuente más elegante diseñado para prevenir la mayoría o todos los cheats que rompen el juego en Minecraft, consumirá significativamente más recursos que el plugin promedio; en casos más graves, el anti-cheat mejorará la experiencia de tus jugadores al banear a los tramposos, pero también la degradará debido al lag que provoca.

### Detalles técnicos
El diseño actual de CloudAC tiene dos partes: el ACTransmitter y el ComputationServer. El ACTransmitter envía todos los datos requeridos al ComputationServer tan pronto como llegan, y el ComputationServer procesa estos datos y los utiliza para ejecutar sus comprobaciones, luego envía los resultados de estas comprobaciones de vuelta al ACTransmitter donde se utilizan en el sistema de castigos; el servidor de cómputo siempre rastrea lo que sucede en el servidor y utiliza la mayoría de las decisiones de diseño y tácticas que ya son utilizadas por cualquier anti-cheat tradicional, teniendo en cuenta que esta explicación está simplificada.

#### Detalles técnicos - Más opciones
Otra ventaja de tener el ComputationServer como un programa separado es que podemos programarlo en cualquier lenguaje de programación que queramos; en este caso, utilicé Go porque es compilado, moderno, un poco más rápido que Java y está orientado a funciones, pero aún tiene cosas como interfaces para que puedas recrear en cierta medida el diseño polimórfico de tu anti-cheat aunque el lenguaje esté orientado a funciones, y ya lo había usado antes; sin embargo, en teoría puedes escribirlo realmente en cualquier lenguaje que desees, otra buena opción sería Rust ya que es moderno, tiene una gran cantidad de funciones y un gran rendimiento.

### ¿Qué pasa con la latencia?
En el caso de CloudAC, la latencia de enviar los datos de los paquetes al programa de cómputo mediante sockets no es un gran problema ya que buscamos un diseño de anti-cheat silencioso. Hay dos formas de configurar un anti-cheat para que opere dependiendo de cuál sea tu prioridad: banear al tramposo o prevenir el cheat. Si tu prioridad es banearlos, es mejor no dejar que el tramposo sepa que está siendo detectado retrasándolo; es mejor "mantenerlos en la oscuridad" y dejar que hagan trampas hasta que tu anti-cheat tenga suficientes datos para banearlos, esta es la táctica que utilizan la mayoría de los servidores grandes y es la que elegimos para CloudAC.

### Contribuciones
CloudAC es un prototipo destinado a mostrar cómo podría funcionar el diseño de este tipo de sistema y nunca tuvo la intención de ser nada más que eso; sin embargo, tenemos la mente abierta y, aunque sin un desarrollo dedicado probablemente nunca se convierta en un anti-cheat listo para producción, agradecemos cualquier contribución que ayude a expandir y mejorar la base de código, lo que proporcionará un mejor ejemplo para la comunidad.

Si estás interesado en ayudarnos a continuar y mejorar CloudAC, aquí está nuestra lista de tareas pendientes (ToDo):
- Actualizar el código de sockets para usar una conexión segura cifrada con TLS
- Mejorar el procesamiento de datos para que sea dinámico y no necesite comprobar el tipo de paquete
- Añadir más comprobaciones e incrementar el procesamiento de datos
- Tus sugerencias e ideas

### Descargo de responsabilidad
No inventé la idea de ejecutar el cerebro de tu anti-cheat separadamente del propio servidor de Minecraft; esa idea ha existido durante algún tiempo y es/fue utilizada por algunos servidores grandes. La idea de CloudAC es hacerla más difundida y conocida por el público y, con suerte, inspirar futuros proyectos a utilizar este diseño superior y, con ello, mejorar las soluciones anti-trampas que vayan saliendo.

#### Descargo de responsabilidad - Estado actual del código
El código actual fue escrito bastante rápido y fue armado a precipitadamente hasta cierto punto ya que originalmente no planeaba publicarlo, estaba destinado a ser más un intento de prueba de concepto, por lo que algunas partes podrían estar un poco descuadradas. He introducido y seguiré introduciendo más limpieza y comentarios, pero creo que ya es decente y definitivamente puede usarse como una buena base; no obstante, siéntete libre de implementar cualquier cambio o mejora que consideres oportuna.
<br/><br/>
<br/><br/>
![resized](https://user-images.githubusercontent.com/24839815/174480405-35d2422c-f1b8-4035-a7c2-ff34a2cfb89a.png)
